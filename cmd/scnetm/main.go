// Command scnetm is the SurvivalcraftNet server-management panel.
//
// This entry point is intentionally thin: it loads the panel configuration,
// opens and migrates the SQLite database, and serves the embedded frontend plus
// a health endpoint. Instance lifecycle, configuration editing, backups and the
// rest of the API are owned by internal/api, which is mounted at the marked
// point below.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"scnetm/internal/auth"
	"scnetm/internal/config"
	"scnetm/internal/store"
	"scnetm/internal/version"
	"scnetm/internal/webui"
)

// shutdownTimeout bounds graceful shutdown so a stuck connection cannot hang
// the process forever.
const shutdownTimeout = 15 * time.Second

// readinessCheckTimeout bounds the post-listen self-check.
const readinessCheckTimeout = 5 * time.Second

// options holds the parsed command-line flags.
type options struct {
	configPath string
	showVer    bool
	listen     string
	dataDir    string
}

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet (config load happens first), so report
		// fatal start-up errors on stderr and let the exit code carry the rest.
		fmt.Fprintf(os.Stderr, "scnetm: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts := parseFlags()

	if opts.showVer {
		fmt.Println(version.String())
		fmt.Println(version.GoVersionLine())
		return nil
	}

	logger := newLogger()
	slog.SetDefault(logger)

	build := version.Get()
	logger.Info("正在启动 scnetm",
		"version", build.Version,
		"commit", build.Commit,
		"built", build.BuildTime,
		"go", build.GoVersion,
		"platform", build.Platform,
		"pid", os.Getpid(),
	)

	// 1. Panel configuration.
	panel, err := config.LoadPanel(opts.configPath)
	if err != nil {
		return err
	}
	// Flags win over the file, so an operator can override without editing it.
	if opts.listen != "" {
		panel.Listen = opts.listen
	}
	if opts.dataDir != "" {
		panel.DataDir = opts.dataDir
		panel.InstancesDir = "" // re-derive from the overridden data_dir
		panel.ApplyDefaults()
	}
	if err := panel.Validate(); err != nil {
		return err
	}

	logger.Info("配置已加载", "config", panel.Redacted())

	// 2. Directories. The panel must start on a clean host, so create them.
	created, err := panel.EnsureDirs()
	if err != nil {
		return err
	}
	if len(created) > 0 {
		logger.Info("已创建目录", "dirs", created)
	}

	// 3. Database: open + migrate. Safe to run on every start.
	dbPath := filepath.Join(panel.DataDir, "scnetm.db")
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		return err
	}

	// Optional: install a known initial administrator password.
	//
	// This is empty by default, in which case the seeded admin keeps the
	// first-run marker and the operator must set a password through the setup
	// screen. That is the safe default and the one docs/README.md documents.
	//
	// When default_admin_password is set, the hash is installed here — and only
	// while the account still has no password, so a value left in config.yaml
	// after the first start cannot overwrite a password chosen in the panel.
	if pw := panel.DefaultAdminPassword; pw != "" {
		hash, err := auth.HashPassword(pw)
		if err != nil {
			// Covers the policy check too, so a weak value fails the start-up
			// rather than being written to the database.
			return fmt.Errorf("default_admin_password: %w", err)
		}
		n, err := store.SeedAdminPassword(db, store.DefaultAdminUsername, hash)
		if err != nil {
			return err
		}
		switch n {
		case 1:
			// Deliberately does not log the password.
			logger.Info("已按配置设置管理员初始密码",
				"username", store.DefaultAdminUsername,
				"hint", "请登录后立即在面板中修改密码")
		case 0:
			// Either the admin already has a password, or the row is gone.
			// Neither is an error, but silently doing nothing would be
			// confusing, so it is reported.
			logger.Info("未设置管理员初始密码：该账户已存在密码，配置值被忽略",
				"username", store.DefaultAdminUsername)
		}
	}

	schemaVersion, err := store.SchemaVersion(db)
	if err != nil {
		return err
	}
	logger.Info("数据库就绪",
		"path", dbPath,
		"schema_version", schemaVersion,
		"latest_schema_version", store.LatestSchemaVersion(),
	)

	if schemaVersion < store.LatestSchemaVersion() {
		return fmt.Errorf("database schema is at v%d but this binary needs v%d",
			schemaVersion, store.LatestSchemaVersion())
	}

	// 4. HTTP handlers.
	handler, shutdownAPI, err := buildHandler(panel, db, logger, build)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: the console WebSocket (owned by internal/api) is a
		// long-lived connection and would be cut off by one.
		BaseContext: func(net.Listener) context.Context { return context.Background() },
		ErrorLog:    slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// 5. Listen. Bind before announcing so a port clash fails loudly and fast.
	ln, err := net.Listen("tcp", panel.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", panel.Listen, err)
	}
	logger.Info("正在监听", "addr", ln.Addr().String(), "url", displayURL(ln.Addr()))

	// 6. Serve until a signal arrives.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil

	case <-ctx.Done():
		stop() // restore default signal handling: a second ^C kills immediately
		logger.Info("收到退出信号，正在排空连接",
			"timeout", shutdownTimeout.String())

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		// Stop supervised instances before draining HTTP, so a game server is
		// never orphaned when the panel exits. Each stop walks the graceful
		// ladder (§5.4), which is what protects the save.
		if err := shutdownAPI(shutdownCtx); err != nil {
			logger.Error("停止受管实例时出错", "error", err)
		}

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("优雅退出失败，强制关闭", "error", err)
			if closeErr := srv.Close(); closeErr != nil {
				return fmt.Errorf("close server: %w", closeErr)
			}
		}
		if err := <-serveErr; err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		logger.Info("已干净退出")
		return nil
	}
}

// parseFlags parses the stdlib flag set. No cobra: the panel has a handful of
// flags and cobra would only add a dependency and an indirection.
func parseFlags() options {
	var opts options
	flag.StringVar(&opts.configPath, "config", "",
		"path to the panel config.yaml (default: ./config.yaml, else built-in defaults)")
	flag.BoolVar(&opts.showVer, "version", false, "print version information and exit")
	flag.StringVar(&opts.listen, "listen", "",
		"override the configured listen address, e.g. 127.0.0.1:8080")
	flag.StringVar(&opts.dataDir, "data-dir", "",
		"override the configured data directory (instances_dir follows it)")
	flag.Parse()
	return opts
}

// newLogger builds the panel's structured logger.
func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if v := os.Getenv("SCNETM_LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			fmt.Fprintf(os.Stderr, "scnetm: ignoring invalid SCNETM_LOG_LEVEL=%q: %v\n", v, err)
		}
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// buildHandler assembles the HTTP surface: /healthz, the REST + WebSocket API,
// and the embedded frontend with SPA fallback. The returned shutdown function
// stops every supervised instance and must be called during graceful shutdown.
func buildHandler(panel *config.Panel, db *sql.DB, logger *slog.Logger, build version.Info) (http.Handler, func(context.Context) error, error) {
	mux := http.NewServeMux()

	// --- Health -----------------------------------------------------------------
	// Deliberately unauthenticated and dependency-light: it must answer on a
	// host with no .NET runtime and no game-server package installed.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": build.Version,
			"commit":  build.Commit,
			"go":      build.GoVersion,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// --- REST + WebSocket API ---------------------------------------------------
	// Mounted BEFORE the "/" SPA fallback below. ServeMux picks the most
	// specific pattern, but registering these first also keeps the intent
	// obvious: an unknown /api/... path must produce a JSON error, never
	// index.html. Without this mount every API call silently returned the SPA
	// shell with HTTP 200, which looks like success to a client.
	shutdownAPI := func(context.Context) error { return nil }
	if apiHandler, stop, err := buildAPI(panel, db, logger, build); err != nil {
		logger.Error("API 未挂载；面板将只提供前端与 /healthz",
			"error", err)
	} else {
		for _, prefix := range apiPrefixes {
			mux.Handle(prefix, apiHandler)
		}
		shutdownAPI = stop
	}

	// --- Frontend ---------------------------------------------------------------
	assets, err := webui.FS()
	if err != nil {
		return nil, nil, err
	}
	frontendBuilt := webui.Available()

	if frontendBuilt {
		logger.Info("正在提供内嵌前端")
	} else {
		logger.Warn("未内嵌前端构建产物；将在 / 提供占位页面" +
			"（运行 `make web && make build` 可嵌入真实界面）")
	}

	mux.Handle("/", spaHandler(assets, frontendBuilt))

	return mux, shutdownAPI, nil
}

// spaHandler serves the embedded frontend with single-page-app fallback:
// any request that does not match a real asset falls back to index.html so the
// client-side router can take over.
func spaHandler(assets fs.FS, built bool) http.Handler {
	fileServer := http.FileServerFS(assets)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !built {
			// Only the API/health surface exists in this build; anything else
			// gets the explanation page rather than a bare 404.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(webui.PlaceholderPage))
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
			// index.html must never be cached, or clients pin an old bundle
			// across upgrades. Hashed assets are immutable.
			if name == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		serveIndex(w, r, assets)
	})
}

// serveIndex writes index.html for a client-side route.
func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "frontend index.html missing from build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// displayURL renders a listener address as a clickable URL.
func displayURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
