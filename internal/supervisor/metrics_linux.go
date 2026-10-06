//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metrics is one sample of an instance's resource usage (§6.6).
type Metrics struct {
	PID        int     `json:"pid"`
	CPUPercent float64 `json:"cpuPercent"`
	RSSBytes   uint64  `json:"rssBytes"`
	VMSize     uint64  `json:"vmSizeBytes"`
	Threads    int     `json:"threads"`

	// FDs is the number of open file descriptors, counted from /proc/<pid>/fd.
	FDs int `json:"fds"`
	// FDMax is the soft RLIMIT_NOFILE, for a "12 / 1024" style display.
	FDMax uint64 `json:"fdMax,omitempty"`

	// ReadBytes/WriteBytes come from /proc/<pid>/io (bytes actually hitting the
	// storage layer, i.e. read_bytes/write_bytes).
	ReadBytes  uint64 `json:"readBytes"`
	WriteBytes uint64 `json:"writeBytes"`

	UptimeSeconds float64 `json:"uptimeSeconds"`
	Restarts      int     `json:"restarts"`
	LastExitCode  *int    `json:"lastExitCode,omitempty"`

	// SampledAt is when this reading was taken.
	SampledAt time.Time `json:"sampledAt"`
	// ClkTck is the clock ticks per second used for the CPU delta.
	ClkTck int64 `json:"-"`
}

// procStats is the raw /proc/<pid>/stat parse result.
type procStats struct {
	pid        int
	comm       string
	state      byte
	utime      uint64 // field 14
	stime      uint64 // field 15
	threads    int    // field 20
	startTicks uint64 // field 22
	vsize      uint64 // field 23
	rssPages   int64  // field 24
}

// collector keeps the previous CPU sample so CPUPercent is a true delta.
type collector struct {
	mu       sync.Mutex
	prevU    uint64
	prevS    uint64
	prevAt   time.Time
	prevPID  int
	clkTck   int64
	pageSize uint64
}

func newCollector() *collector {
	return &collector{
		clkTck:   int64(clkTck()),
		pageSize: uint64(os.Getpagesize()),
	}
}

// Sample collects metrics for pid. It returns ErrProcessExited (wrapped) when
// /proc/<pid> is gone, and never panics on a partially readable procfs.
func (c *collector) Sample(pid int, restarts int, lastExit *int) (Metrics, error) {
	if pid <= 0 {
		return Metrics{}, fmt.Errorf("%w: invalid pid %d", ErrProcessExited, pid)
	}
	now := time.Now()
	st, err := readProcStat(pid)
	if err != nil {
		return Metrics{}, err
	}

	m := Metrics{
		PID:           pid,
		Threads:       st.threads,
		VMSize:        st.vsize,
		UptimeSeconds: ticksToSeconds(now, bootTime, st.startTicks, c.clkTck),
		Restarts:      restarts,
		SampledAt:     now,
		ClkTck:        c.clkTck,
	}
	if st.rssPages > 0 {
		m.RSSBytes = uint64(st.rssPages) * c.pageSize
	}

	// CPU%: delta of (utime+stime) over wall time, in clock ticks.
	c.mu.Lock()
	if c.prevPID == pid && !c.prevAt.IsZero() {
		dCPU := float64((st.utime + st.stime) - (c.prevU + c.prevS))
		dWall := now.Sub(c.prevAt).Seconds()
		if dWall > 0 {
			m.CPUPercent = dCPU / float64(c.clkTck) / dWall * 100
			if m.CPUPercent < 0 {
				m.CPUPercent = 0 // counter went backwards (pid reuse)
			}
		}
	}
	c.prevPID, c.prevU, c.prevS, c.prevAt = pid, st.utime, st.stime, now
	c.mu.Unlock()

	// Secondary sources: failures here degrade the sample, they do not fail it.
	if fds, err := countFDs(pid); err == nil {
		m.FDs = fds
	}
	m.FDMax = rlimitNoFile()
	if r, w, err := readProcIO(pid); err == nil {
		m.ReadBytes, m.WriteBytes = r, w
	}
	if lastExit != nil {
		v := *lastExit
		m.LastExitCode = &v
	}
	return m, nil
}

// Reset drops the stored CPU baseline (call on start/stop so a restarted pid
// does not inherit the previous process's counters).
func (c *collector) Reset() {
	c.mu.Lock()
	c.prevPID, c.prevU, c.prevS, c.prevAt = 0, 0, 0, time.Time{}
	c.mu.Unlock()
}

// ticksToSeconds converts a starttime (in clock ticks since boot) plus the
// cached boot time into a process age in seconds.
func ticksToSeconds(now, boot time.Time, startTicks uint64, clk int64) float64 {
	if clk <= 0 || startTicks == 0 {
		return 0
	}
	started := boot.Add(time.Duration(startTicks) * time.Second / time.Duration(clk))
	d := now.Sub(started).Seconds()
	if d < 0 {
		return 0
	}
	return d
}

// readProcStat parses /proc/<pid>/stat.
//
// The comm field (2) is wrapped in parentheses and may itself contain spaces and
// parentheses, so the fields after it are parsed from the LAST ')' backwards —
// this is what makes the parser robust across comm contents and 32-bit layouts.
func readProcStat(pid int) (procStats, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStats{}, fmt.Errorf("%w: %v", ErrProcessExited, err)
	}
	return parseProcStat(pid, string(b))
}

// parseProcStat is split out so it can be unit-tested against fixture strings.
func parseProcStat(pid int, s string) (procStats, error) {
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < 0 || close < open {
		return procStats{}, fmt.Errorf("%w: malformed stat for pid %d", ErrProcessExited, pid)
	}
	st := procStats{pid: pid, comm: s[open+1 : close]}

	// Fields 3.. are whitespace separated after "pid (comm) ".
	rest := strings.Fields(strings.TrimSpace(s[close+1:]))
	// rest[i] corresponds to stat field (i+3).
	get := func(field int) (string, bool) {
		i := field - 3
		if i < 0 || i >= len(rest) {
			return "", false
		}
		return rest[i], true
	}
	if v, ok := get(3); ok && len(v) > 0 {
		st.state = v[0]
	}
	st.utime = parseUint(get2(get, 14))
	st.stime = parseUint(get2(get, 15))
	st.threads = int(parseUint(get2(get, 20)))
	st.startTicks = parseUint(get2(get, 22))
	st.vsize = parseUint(get2(get, 23))
	st.rssPages = int64(parseUint(get2(get, 24)))
	return st, nil
}

// get2 is a tiny helper so the field list above stays a readable table.
func get2(get func(int) (string, bool), field int) string {
	v, _ := get(field)
	return v
}

func parseUint(s string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// countFDs counts entries in /proc/<pid>/fd. A permission error is reported so
// the caller can distinguish "0" from "unknown".
func countFDs(pid int) (int, error) {
	d, err := os.Open(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrProcessExited, err)
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0, err
	}
	return len(names), nil
}

// readProcIO reads /proc/<pid>/io. read_bytes/write_bytes are the fields that
// actually reached the block layer; rchar/wchar (which include tty I/O) are
// deliberately not used.
func readProcIO(pid int) (read, write uint64, err error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrProcessExited, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "read_bytes":
			read = parseUint(v)
		case "write_bytes":
			write = parseUint(v)
		}
	}
	return read, write, nil
}

// Sample collects metrics for the running instance. When the instance is not
// running it returns ErrNotRunning (or ErrProcessExited if the pid vanished
// between the state check and the read) along with uptime/restart metadata.
func (r *Runner) Sample() (Metrics, error) {
	r.mu.RLock()
	pid := r.pid
	coll := r.collector
	r.mu.RUnlock()

	snap := r.sm.snapshot()
	if pid <= 0 {
		m := Metrics{
			Restarts:      snap.Restarts,
			LastExitCode:  snap.ExitCode,
			SampledAt:     time.Now(),
			UptimeSeconds: 0,
		}
		return m, fmt.Errorf("%w (state %s)", ErrNotRunning, snap.State)
	}
	if coll == nil {
		return Metrics{}, fmt.Errorf("%w: metrics collector not initialised", ErrProcessExited)
	}
	return coll.Sample(pid, snap.Restarts, snap.ExitCode)
}

// SampleMetrics is a package-level convenience for callers that hold a Runner
// but want the error to be ErrProcessExited on a dead process.
func SampleMetrics(r *Runner) (Metrics, error) { return r.Sample() }

// errIsExited reports whether err means "the process is gone".
func errIsExited(err error) bool {
	return errors.Is(err, ErrProcessExited) || errors.Is(err, ErrNotRunning)
}
