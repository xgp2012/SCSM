package supervisor

// Colour modes. The plan calls them "基础终端模式" (basic) and "增强终端模式"
// (enhanced); see §5.2.
const (
	// ColorModeEnhanced requests a real PTY, which is what unlocks ANSI colour,
	// terminal sizing and the server's safe-shutdown path.
	ColorModeEnhanced = "enhanced"

	// ColorModeBasic forces pipes: no colour, no interactive terminal. Useful
	// for containers without tty devices and for log-only replicas.
	ColorModeBasic = "basic"

	// ColorModeAuto tries a PTY and falls back to pipes, reporting the
	// degradation as an event.
	ColorModeAuto = "auto"
)
