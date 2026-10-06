//go:build linux

package supervisor

import (
	"os"

	"golang.org/x/sys/unix"
)

// defaultClkTck is USER_HZ. Every mainstream Linux architecture uses 100, and
// /proc/<pid>/stat is documented in USER_HZ units.
const defaultClkTck = 100

// clkTck returns the kernel clock ticks per second, used to convert
// /proc/<pid>/stat utime/stime/starttime into seconds.
//
// x/sys/unix does not export Sysconf, so the value is read from the kernel
// auxv vector, which is authoritative and free of parsing heuristics. If that
// fails we fall back to USER_HZ.
func clkTck() int64 {
	if b, err := os.ReadFile("/proc/self/auxv"); err == nil {
		if v, ok := auxvClockTick(b); ok && v > 0 {
			return v
		}
	}
	return defaultClkTck
}

// auxvClockTick scans the ELF auxiliary vector for AT_CLKTCK (17).
// Each entry is two 64-bit native-endian words: a_type, a_val.
func auxvClockTick(b []byte) (int64, bool) {
	const atClkTck = 17
	for i := 0; i+16 <= len(b); i += 16 {
		typ := nativeUint64(b[i : i+8])
		val := nativeUint64(b[i+8 : i+16])
		if typ == 0 {
			break
		}
		if typ == atClkTck {
			return int64(val), true
		}
	}
	return 0, false
}

// nativeUint64 decodes a native-endian word. isBigEndian comes from a build
// tag file so the assumption is explicit rather than accidental.
func nativeUint64(b []byte) uint64 {
	if isBigEndian {
		return uint64(b[7]) | uint64(b[6])<<8 | uint64(b[5])<<16 | uint64(b[4])<<24 |
			uint64(b[3])<<32 | uint64(b[2])<<40 | uint64(b[1])<<48 | uint64(b[0])<<56
	}
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

// rlimitNoFile exposes the soft RLIMIT_NOFILE for "fds / max" displays.
func rlimitNoFile() uint64 {
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rl); err != nil {
		return 0
	}
	return rl.Cur
}

// rlimitNProc exposes the soft RLIMIT_NPROC, for the optional resource panel.
func rlimitNProc() uint64 {
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &rl); err != nil {
		return 0
	}
	return rl.Cur
}
