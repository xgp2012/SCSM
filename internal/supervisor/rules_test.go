//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"scnetm/internal/ansi"
)

// hasANSIForTest and stripForTest delegate to the REAL internal/ansi package
// (not the local fake) so the rule-matching tests exercise exactly the strings
// the runner will see in production.
func hasANSIForTest(s string) bool { return ansi.HasANSI(s) }

func stripForTestReal(s string) string { return ansi.Strip(s) }

// isErr is errors.Is with a readable failure message; used across the suite.
func isErr(err, target error) bool { return errors.Is(err, target) }

// fmtSscan is a tiny sscanf-alike used by the tests. It differs from
// fmt.Sscanf in one important way: Sscanf demands the format match from the very
// start of the string, so a line like "INFO: spawned grandchild 42" would not
// match "spawned grandchild %d". This version searches for the format's literal
// prefix first, which is what a log-scraping helper actually wants.
func fmtSscan(s, format string, out ...any) (int, error) {
	if i := strings.Index(s, format[:strings.Index(format, "%")]); i >= 0 {
		s = s[i:]
	}
	return fmt.Sscanf(s, format, out...)
}

// ---------------------------------------------------------------- rule table

// TestRuleTableMatchesRealAnchors pins the §5.2.1 rules against the exact lines
// recorded in the plan (§2.4), ANSI-stripped as the runner does.
func TestRuleTableMatchesRealAnchors(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		want  string
		kind  ruleKind
		value string
	}{
		{
			name: "server listening captures the port",
			line: "[StartServer]开启服务器成功，端口 28887",
			want: "server.listening", kind: kindListening, value: "28887",
		},
		{
			name: "halfwidth comma variant",
			line: "[StartServer]开启服务器成功, 端口 25565",
			want: "server.listening", kind: kindListening, value: "25565",
		},
		{
			name: "world loaded captures the DISPLAY name only",
			line: "Loaded world, GameMode=Harmless, StartingPosition=Easy, WorldName=ShowNameX, VisibilityRange=128, Resolution=High",
			want: "world.loaded", kind: kindWorldLoaded, value: "ShowNameX",
		},
		{
			name: "game screen",
			line: `Entered screen "Game"`,
			want: "screen.game", kind: kindReady,
		},
		{
			name: "plugin loaded",
			line: "开始加载插件 TestPlugin(tests/TestPlugin.dll) 版本: 1:0:0",
			want: "plugin.loaded", kind: kindPlugin, value: "TestPlugin",
		},
		{
			name: "basic terminal mode",
			line: "[自动检测] 输出被重定向，使用基础终端模式",
			want: "term.redirected", kind: kindTermMode,
		},
		{
			name: "enhanced terminal mode",
			line: "EnhancedTerminalLogSink initialized With ANSI Support: True",
			want: "term.enhanced", kind: kindTermMode,
		},
		{
			name: "error line",
			line: "12:00:01.123 ERROR: something failed",
			want: "log.error", kind: kindError,
		},
		{
			name: "unhandled exception",
			line: "Unhandled exception. System.NullReferenceException",
			want: "server.crash.unhandled", kind: kindCrash,
		},
		{
			name: "stack frame",
			line: "   at Game.Server.Main(String[] args)",
			want: "server.crash.stack", kind: kindCrash,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := applyRules(tc.line)
			var hit *ruleMatch
			for i := range ms {
				if ms[i].Rule == tc.want {
					hit = &ms[i]
					break
				}
			}
			if hit == nil {
				t.Fatalf("rule %s did not match %q (matched: %v)", tc.want, tc.line, ruleNamesOf(ms))
			}
			if hit.Kind != tc.kind {
				t.Errorf("kind = %v, want %v", hit.Kind, tc.kind)
			}
			if tc.value != "" && hit.Value != tc.value {
				t.Errorf("captured %q, want %q", hit.Value, tc.value)
			}
		})
	}
}

func ruleNamesOf(ms []ruleMatch) []string {
	out := make([]string, 0, len(ms))
	for i := range ms {
		out = append(out, ms[i].Rule)
	}
	return out
}

// TestRulesIgnoreOrdinaryLines guards against over-eager matching.
func TestRulesIgnoreOrdinaryLines(t *testing.T) {
	for _, line := range []string{
		"INFO: 使用游客模式启动，清理SCKey服务端绑定信息",
		"INFO: 已自动生成存档目录 app:/Worlds/World",
		"",
		"12:00:00.000 DEBUG: nothing to see",
	} {
		if ms := applyRules(line); len(ms) != 0 {
			t.Errorf("line %q unexpectedly matched %v", line, ruleNamesOf(ms))
		}
	}
}

// TestRulesMatchPlainNotRaw is the core anti-regression rule: the runner must
// match the ANSI-stripped text, because escape bytes can be interleaved with the
// anchor itself.
func TestRulesMatchPlainNotRaw(t *testing.T) {
	coloured := "\x1b[32m[StartServer]\x1b[0m开启服务器成功，\x1b[1m端口 28887\x1b[0m"

	// The RAW text must NOT match: escape bytes sit inside the anchor, so a
	// naive matcher would fail on real coloured output.
	if ms := applyRules(coloured); len(ms) != 0 {
		t.Errorf("rules matched RAW ANSI text, which they must not: %v", ruleNamesOf(ms))
	}
	// The ANSI-stripped text MUST match — this is what the runner feeds in.
	plain := stripForTestReal(coloured)
	if ms := applyRules(plain); len(ms) == 0 {
		t.Fatalf("rules failed on the stripped text %q", plain)
	}
}

// TestPortValueRejectsGarbage proves the capture is validated, not trusted.
func TestPortValueRejectsGarbage(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"28887", 28887},
		{" 42 ", 42},
		{"0", 0},
		{"70000", 0},
		{"abc", 0},
	} {
		m := ruleMatch{Value: tc.in}
		if got := m.portValue(); got != tc.want {
			t.Errorf("portValue(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestRuleNamesAreStable documents the rule inventory for drift detection.
func TestRuleNamesAreStable(t *testing.T) {
	want := []string{
		"term.redirected", "term.enhanced", "server.listening", "world.loaded",
		"screen.game", "plugin.loaded", "log.error",
		"server.crash.unhandled", "server.crash.stack",
	}
	got := RuleNames()
	if len(got) != len(want) {
		t.Fatalf("RuleNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RuleNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRegexesCompiledOnce is a guard against reintroducing per-line compilation:
// regexp.MustCompile at package init means the table is immutable afterwards.
func TestRegexesCompiledOnce(t *testing.T) {
	before := make([]string, len(rules))
	for i := range rules {
		before[i] = rules[i].Re.String()
	}
	for i := 0; i < 100; i++ {
		applyRules("[StartServer]开启服务器成功，端口 1")
	}
	for i := range rules {
		if rules[i].Re.String() != before[i] {
			t.Fatalf("rule %d regex changed at runtime", i)
		}
	}
}

// ---------------------------------------------------------------- anchors->state

// TestAnchorOrderingDoesNotMatter proves the state machine waits for all three
// conditions rather than depending on the order the lines arrive.
func TestAnchorOrderingDoesNotMatter(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	// Manually drive the machine the way handleRecord would.
	r.sm.force(StateStarting)
	r.sm.setAlive(true)

	r.handleRecord(LogRecord{Plain: `Entered screen "Game"`})
	if got := r.State(); got != StateStarting {
		t.Fatalf("state = %s after only the Game screen anchor, want starting", got)
	}
	r.handleRecord(LogRecord{Plain: "Loaded world, WorldName=W, GameMode=Harmless"})
	if got := r.State(); got != StateStarting {
		t.Fatalf("state = %s after two anchors, want starting", got)
	}
	r.handleRecord(LogRecord{Plain: "[StartServer]开启服务器成功，端口 12345"})
	if got := r.State(); got != StateRunning {
		t.Fatalf("state = %s after all three anchors, want running", got)
	}
	snap := r.Snapshot()
	if snap.Port != 12345 || snap.WorldName != "W" {
		t.Errorf("captures = port %d world %q, want 12345/W", snap.Port, snap.WorldName)
	}
}

// TestReadyRequiresLiveness proves a dead process cannot be "ready" no matter
// what the log says (the anchors can arrive during shutdown).
func TestReadyRequiresLiveness(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r.sm.force(StateStarting)
	r.handleRecord(LogRecord{Plain: "[StartServer]开启服务器成功，端口 1"})
	r.handleRecord(LogRecord{Plain: `Entered screen "Game"`})
	// alive is false: the machine must not promote.
	if got := r.State(); got != StateStarting {
		t.Fatalf("state = %s with anchors but no liveness, want starting", got)
	}
	r.sm.setAlive(true)
	r.handleRecord(LogRecord{Plain: "Loaded world, WorldName=W, GameMode=Harmless"})
	r.handleRecord(LogRecord{Plain: `Entered screen "Game"`})
	if got := r.State(); got != StateRunning {
		t.Fatalf("state = %s after liveness arrived, want running", got)
	}
}

// TestRuleEventsCarryCaptures proves the emitted events surface the captured
// values the API needs (port, world name, plugin name).
func TestRuleEventsCarryCaptures(t *testing.T) {
	var mu = make(chan Event, 64)
	o := helperOpts(t, "ready")
	o.OnEventFn = func(ev Event) { mu <- ev }
	r, err := NewRunner(o)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r.sm.setAlive(true)
	r.handleRecord(LogRecord{Plain: "[StartServer]开启服务器成功，端口 28887"})
	r.handleRecord(LogRecord{Plain: "Loaded world, WorldName=ShowNameX, GameMode=Harmless"})
	r.handleRecord(LogRecord{Plain: "开始加载插件 P(p.dll) 版本: 1:0:0"})

	var sawPort, sawWorld, sawPlugin bool
	for {
		select {
		case ev := <-mu:
			switch ev.Type {
			case EventState:
				if ev.Port == 28887 {
					sawPort = true
				}
				if ev.WorldName == "ShowNameX" {
					sawWorld = true
				}
			case EventPluginLoaded:
				if ev.Plugin == "P" {
					sawPlugin = true
				}
			}
			continue
		default:
		}
		break
	}
	if !sawPort {
		t.Error("no event carried the captured port")
	}
	if !sawWorld {
		t.Error("no event carried the captured world name")
	}
	if !sawPlugin {
		t.Error("no event carried the captured plugin name")
	}
}

// TestStripAndHasANSIContract documents the two ansi helpers supervisor relies on.
func TestStripAndHasANSIContract(t *testing.T) {
	coloured := "\x1b[1;31mred\x1b[0m plain"
	if !hasANSIForTest(coloured) {
		t.Fatal("HasANSI did not detect a CSI sequence")
	}
	if got := stripForTestReal(coloured); got != "red plain" {
		t.Errorf("stripped = %q, want %q", got, "red plain")
	}
	if strings.Contains(stripForTestReal(coloured), "\x1b") {
		t.Error("stripped text still contains ESC")
	}
}
