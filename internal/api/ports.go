package api

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// Port pool defaults. The game listens on a single UDP port
// (LiteNetLib/ENet — §2.6), so the allocator works exclusively in UDP space.
const (
	// DefaultPortPoolStart is the low end of the allocation pool.
	DefaultPortPoolStart = 20000
	// DefaultPortPoolEnd is the high end (inclusive).
	DefaultPortPoolEnd = 20100
)

// PortPool is an inclusive range of candidate UDP ports.
type PortPool struct {
	Start int
	End   int
}

// Valid reports whether the pool is usable.
func (p PortPool) Valid() bool { return p.Start > 0 && p.End >= p.Start && p.End <= 65535 }

// Size returns the number of ports in the pool.
func (p PortPool) Size() int {
	if !p.Valid() {
		return 0
	}
	return p.End - p.Start + 1
}

// Contains reports whether port is inside the pool.
func (p PortPool) Contains(port int) bool {
	return p.Valid() && port >= p.Start && port <= p.End
}

// UDPPortAllocator picks a free UDP port for a new instance.
//
// # Why UDP matters
//
// The plan calls this out explicitly (§2.6, §6.6): the server speaks UDP via
// LiteNetLib. Checking TCP occupancy — the reflexive thing to do, because every
// port-scanner example does it — is wrong in both directions:
//
//   - A port can be TCP-free but UDP-taken, so the check passes and the instance
//     fails at bind time with a confusing error.
//   - A port can be UDP-free but TCP-taken (e.g. by an unrelated web service),
//     so a correct allocation is rejected for no reason.
//
// Every probe here therefore uses net.ListenPacket("udp", ...), and the port is
// released immediately after the probe so the real game process can take it.
//
// The probe binds to the wildcard address because the game server does the
// same; binding to 127.0.0.1 would miss a conflict on 0.0.0.0.
type UDPPortAllocator struct {
	pool PortPool
	// probe binds a port to test availability. Injectable for tests.
	probe func(port int) error
	mu    sync.Mutex
}

// NewUDPPortAllocator builds an allocator over pool.
func NewUDPPortAllocator(pool PortPool) *UDPPortAllocator {
	if !pool.Valid() {
		pool = PortPool{Start: DefaultPortPoolStart, End: DefaultPortPoolEnd}
	}
	return &UDPPortAllocator{pool: pool, probe: probeUDP}
}

// Compile-time assertion.
var _ PortAllocator = (*UDPPortAllocator)(nil)

// Pool returns the configured range.
func (a *UDPPortAllocator) Pool() PortPool { return a.pool }

// IsFree reports whether a UDP port can currently be bound.
func (a *UDPPortAllocator) IsFree(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	return a.probe(port) == nil
}

// Allocate returns the lowest free port in the pool that is not in used.
//
// Lowest-first keeps allocations stable and predictable, which matters for
// operators who port-forward by hand. When every port in the pool is taken,
// ErrConflict is returned so the handler can answer 409 with a clear message
// rather than a 500.
func (a *UDPPortAllocator) Allocate(_ context.Context, used []int) (int, error) {
	if !a.pool.Valid() {
		return 0, fmt.Errorf("%w: port pool is not configured", ErrInvalid)
	}

	usedSet := make(map[int]struct{}, len(used))
	for _, p := range used {
		usedSet[p] = struct{}{}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var probed int
	for port := a.pool.Start; port <= a.pool.End; port++ {
		if _, taken := usedSet[port]; taken {
			continue
		}
		probed++
		if a.probe(port) == nil {
			return port, nil
		}
	}

	if probed == 0 {
		return 0, fmt.Errorf("%w: every port in the pool %d-%d is already assigned to an instance",
			ErrConflict, a.pool.Start, a.pool.End)
	}
	return 0, fmt.Errorf("%w: no free UDP port in the pool %d-%d (%d candidate ports all bound)",
		ErrConflict, a.pool.Start, a.pool.End, probed)
}

// probeUDP attempts to bind a UDP port on the wildcard address and release it.
func probeUDP(port int) error {
	conn, err := net.ListenPacket("udp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return conn.Close()
}

// SetProbeForTest replaces the availability probe. Test helper.
func (a *UDPPortAllocator) SetProbeForTest(fn func(port int) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.probe = fn
}

// ValidatePort checks a client-supplied port number.
//
// Ports below 1024 are rejected: binding them requires privileges, and a panel
// that silently fails to start an instance because of a privileged port is a
// bad experience. The message says so explicitly.
func ValidatePort(port int) error {
	switch {
	case port <= 0:
		return fmt.Errorf("%w: port must be a positive integer", ErrInvalid)
	case port < 1024:
		return fmt.Errorf("%w: port %d is in the privileged range (1-1023)", ErrInvalid, port)
	case port > 65535:
		return fmt.Errorf("%w: port %d is above 65535", ErrInvalid, port)
	}
	return nil
}

// CheckPortFree is a convenience used by the create handler to give a precise
// error when a user-supplied port is already bound in UDP space.
func CheckPortFree(port int) error {
	if err := probeUDP(port); err != nil {
		return fmt.Errorf("%w: UDP port %d is already in use", ErrConflict, port)
	}
	return nil
}

// PortInUseTTL is how long a port allocation is remembered by a
// PortReservation, used to stop two concurrent creates from racing onto the
// same port before either has committed its instance row.
type PortReservation struct {
	mu   sync.Mutex
	held map[int]time.Time
	ttl  time.Duration
	now  func() time.Time
}

// NewPortReservation builds a reservation table.
func NewPortReservation(ttl time.Duration) *PortReservation {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &PortReservation{held: make(map[int]time.Time), ttl: ttl, now: time.Now}
}

// Reserve records a port as taken, returning false if it is already reserved.
func (r *PortReservation) Reserve(port int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	if until, ok := r.held[port]; ok && r.now().Before(until) {
		return false
	}
	r.held[port] = r.now().Add(r.ttl)
	return true
}

// Release drops a reservation (after the instance row is committed, or on
// failure).
func (r *PortReservation) Release(port int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.held, port)
}

// Held returns the currently reserved ports.
func (r *PortReservation) Held() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	out := make([]int, 0, len(r.held))
	for p := range r.held {
		out = append(out, p)
	}
	return out
}

func (r *PortReservation) sweepLocked() {
	now := r.now()
	for p, until := range r.held {
		if now.After(until) {
			delete(r.held, p)
		}
	}
}
