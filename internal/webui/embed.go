// Package webui embeds the built frontend and exposes it as an fs.FS.
//
// # Why the frontend lives under internal/webui/dist
//
// go:embed cannot reference paths outside the directory containing the
// directive — "../../web/dist" is a compile error. The frontend is therefore
// built into web/dist (by the web/ toolchain, which is where Vite wants it) and
// *copied* into internal/webui/dist by the Makefile's build target, which then
// runs go build. internal/webui/dist is a build artefact and is git-ignored
// apart from its .gitkeep.
//
// The result is still a single self-contained binary, which is the point of the
// embed in the first place.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"strings"
)

// embedded holds the copied frontend build.
//
// The pattern is "all:dist" rather than "dist" so that dotfiles and files
// beginning with "_" or "." (Vite emits some) are included.
//
//go:embed all:dist
var embedded embed.FS

// distRoot is the directory name inside the embedded filesystem.
const distRoot = "dist"

// ErrNotBuilt is returned by [FS] when the binary was compiled without a
// frontend build, i.e. internal/webui/dist only contains the .gitkeep
// placeholder.
var ErrNotBuilt = errors.New("webui: frontend not built; run `make web` then `make build`")

// Available reports whether a real frontend build is embedded.
//
// It is false when the directory contains nothing but the placeholder, which is
// the normal state of a fresh checkout before the frontend has been built. main
// uses it to serve an explanatory page instead of failing to start.
func Available() bool {
	f, err := FS()
	if err != nil {
		return false
	}
	return isBuilt(f)
}

// FS returns the embedded frontend rooted at the dist directory, so that
// fs.ReadFile(fsys, "index.html") reads the built index page.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(embedded, distRoot)
	if err != nil {
		return nil, errors.Join(ErrNotBuilt, err)
	}
	return sub, nil
}

// isBuilt reports whether the filesystem contains more than placeholders.
//
// Any non-placeholder regular file counts as "built": Vite always emits
// index.html, so its presence is necessary and sufficient in practice, but
// scanning for the first real file also covers a partially built directory.
func isBuilt(fsys fs.FS) bool {
	found := false
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries simply do not count
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".gitkeep" || name == ".gitignore" || strings.HasPrefix(name, ".") {
			return nil
		}
		found = true
		return fs.SkipAll
	})
	return found
}

// PlaceholderPage is served at "/" when no frontend build is embedded, so an
// operator who forgets `make web` sees an explanation rather than a blank 404.
const PlaceholderPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>scnetm — 前端尚未构建</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; padding: 3rem 1.5rem; background: #18181b; color: #e4e4e7;
         font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 44rem; margin: 0 auto; }
  h1 { font-size: 1.5rem; margin: 0 0 .25rem; letter-spacing: -0.01em; }
  p.sub { color: #a1a1aa; margin: 0 0 2rem; }
  pre { background: #09090b; border: 1px solid #27272a; border-radius: .5rem;
        padding: 1rem; overflow-x: auto; color: #d4d4d8; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .875rem; }
  .ok { color: #4ade80; }
  ul { color: #a1a1aa; }
</style>
</head>
<body>
<main>
  <h1>scnetm 面板正在运行</h1>
  <p class="sub">API 已就绪，但当前二进制中未内嵌前端构建产物。</p>
  <p><span class="ok">&#10003;</span> <a href="/healthz" style="color:#60a5fa">/healthz</a> 可访问，并会返回面板版本。</p>
  <p>请先构建前端，再重新构建面板：</p>
  <pre><code>make web      # 用 Vite 构建 web/dist
make build    # 将 web/dist 复制到 internal/webui/dist，然后 go build</code></pre>
  <p>否则面板将以无界面模式运行，用于部署检查没有问题：</p>
  <ul>
    <li>启动面板本身不需要 <code>.NET 10</code> 运行时，也不需要服务端程序包</li>
    <li>在两者都安装之前，实例无法启动</li>
  </ul>
</main>
</body>
</html>
`
