package supervisor

import (
	"regexp"
	"strconv"
	"strings"
)

// ruleKind classifies what a matched rule means to the state machine.
type ruleKind int

const (
	kindInfo ruleKind = iota
	kindTermMode
	kindListening
	kindWorldLoaded
	kindReady
	kindPlugin
	kindError
	kindCrash
)

// rule is one compiled log-parsing rule (plan §5.2.1).
//
// All regexes are compiled exactly once, at package init, and matched against
// the ANSI-stripped Plain text only. Matching never compiles anything.
type rule struct {
	Name  string
	Kind  ruleKind
	Re    *regexp.Regexp
	Mode  string // for kindTermMode: "basic" | "enhanced"
	Group string // for capture rules: the value used as the event payload
}

// The rule table. Order matters only for the emitted event sequence per line.
var rules = compileRules([]rule{
	{
		Name: "term.redirected",
		Kind: kindTermMode,
		Mode: "basic",
		Re:   regexp.MustCompile(`\[自动检测\]\s*输出被重定向`),
	},
	{
		Name: "term.enhanced",
		Kind: kindTermMode,
		Mode: "enhanced",
		Re:   regexp.MustCompile(`EnhancedTerminalLogSink`),
	},
	{
		Name: "server.listening",
		Kind: kindListening,
		// e.g. "[StartServer]开启服务器成功，端口 28887"
		Re:    regexp.MustCompile(`\[StartServer\]\s*开启服务器成功[，,]\s*端口\s*(\d+)`),
		Group: "port",
	},
	{
		Name: "world.loaded",
		Kind: kindWorldLoaded,
		// e.g. "Loaded world, GameMode=Harmless, ..., WorldName=ShowNameX, ..."
		// WorldName is the *display* name; the directory comes from WorldPath
		// and must never be derived from this value (plan §2.7).
		Re:    regexp.MustCompile(`Loaded world,[^\n]*?WorldName=([^,\s]+)`),
		Group: "worldName",
	},
	{
		Name: "screen.game",
		Kind: kindReady,
		Re:   regexp.MustCompile(`Entered screen\s+"Game"`),
	},
	{
		Name: "plugin.loaded",
		Kind: kindPlugin,
		// e.g. "开始加载插件 XXX(...) 版本: 1:0:0"
		Re:    regexp.MustCompile(`开始加载插件\s+(.+?)\((.+?)\)\s*版本:\s*(\d+):(\d+):(\d+)`),
		Group: "plugin",
	},
	{
		Name: "log.error",
		Kind: kindError,
		Re:   regexp.MustCompile(`\bERROR:`),
	},
	{
		Name: "server.crash.unhandled",
		Kind: kindCrash,
		Re:   regexp.MustCompile(`Unhandled exception`),
	},
	{
		Name: "server.crash.stack",
		Kind: kindCrash,
		Re:   regexp.MustCompile(`\bat Game\.`),
	},
})

// compileRules is a separate function so every regexp.MustCompile happens at
// init time in one obvious place.
func compileRules(rs []rule) []rule { return rs }

// applyRules runs the rule table against one ANSI-stripped line and returns the
// classified matches. It performs no compilation and allocates nothing when no
// rule matches.
func applyRules(plain string) []ruleMatch {
	var out []ruleMatch
	for i := range rules {
		ru := &rules[i]
		m := ru.Re.FindStringSubmatch(plain)
		if m == nil {
			continue
		}
		rm := ruleMatch{Rule: ru.Name, Kind: ru.Kind, Mode: ru.Mode, Groups: m[1:]}
		if ru.Group != "" && len(m) > 1 {
			rm.Value = m[1]
		}
		out = append(out, rm)
	}
	return out
}

// ruleMatch is one rule hit on one line.
type ruleMatch struct {
	Rule   string
	Kind   ruleKind
	Mode   string
	Value  string
	Groups []string
}

// portValue parses a captured port, returning 0 when out of range.
func (m ruleMatch) portValue() int {
	n, err := strconv.Atoi(strings.TrimSpace(m.Value))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

// RuleNames lists the compiled rule names, in table order. Exposed for the API
// layer's diagnostics endpoint and for documentation drift tests.
func RuleNames() []string {
	out := make([]string, 0, len(rules))
	for i := range rules {
		out = append(out, rules[i].Name)
	}
	return out
}
