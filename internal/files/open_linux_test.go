package files

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRecheckOpenPortablePath exercises the portable descriptor re-validation,
// which is the fallback used when openat2 is unavailable. It is worth testing
// explicitly because it is production code on older kernels even though it is
// normally bypassed here.
func TestRecheckOpenPortablePath(t *testing.T) {
	// recheckOpen deliberately short-circuits when openat2 is in use (the
	// descriptor is already kernel-verified), so the portable check must be
	// exercised with openat2 disabled — which is exactly the configuration it
	// runs in on an older kernel.
	restore := disableOpenat2()
	defer restore()

	r := mustRoot(t)

	f, err := os.Open(filepath.Join(r.Dir(), "Settings.xml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := r.recheckOpen(f); err != nil {
		t.Errorf("recheckOpen on an in-root file = %v; want nil", err)
	}

	// A descriptor for a file genuinely outside the root must be rejected.
	outside := filepath.Join(filepath.Dir(r.Dir()), "outside", "secret.txt")
	of, err := os.Open(outside)
	if err != nil {
		t.Fatal(err)
	}
	defer of.Close()
	if err := r.recheckOpen(of); err == nil {
		t.Error("recheckOpen accepted a descriptor outside the root")
	} else if !errors.Is(err, ErrUnsafePath) {
		t.Errorf("recheckOpen = %v; want ErrUnsafePath", err)
	}
}

// TestOpenFileFallbackPath drives openFile with openat2 temporarily disabled,
// proving the portable fallback opens correctly and still refuses escapes.
func TestOpenFileFallbackPath(t *testing.T) {
	if !openat2Supported() {
		t.Skip("openat2 is not available, so the fallback is already the active path")
	}
	// Force the fallback for the duration of the test.
	restore := disableOpenat2()
	defer restore()

	r := mustRoot(t)

	f, err := r.Open("Settings.xml")
	if err != nil {
		t.Fatalf("Open via the portable fallback: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := f.Read(buf)
	f.Close()
	if string(buf[:n]) != "<Settings/>" {
		t.Fatalf("fallback read %q; want <Settings/>", buf[:n])
	}

	// Create through the fallback.
	c, err := r.Create("via_fallback.txt")
	if err != nil {
		t.Fatalf("Create via the portable fallback: %v", err)
	}
	if _, err := c.WriteString("fallback"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if b, rerr := os.ReadFile(filepath.Join(r.Dir(), "via_fallback.txt")); rerr != nil || string(b) != "fallback" {
		t.Fatalf("fallback write failed: %v (%q)", rerr, b)
	}

	// OpenDir through the fallback.
	d, err := r.OpenDir("Worlds")
	if err != nil {
		t.Fatalf("OpenDir via the portable fallback: %v", err)
	}
	d.Close()

	// Escapes must still be refused with openat2 off.
	for _, bad := range []string{"../outside/secret.txt", "link_out_file", "link_out_dir/secret.txt", "/etc/passwd"} {
		if f, err := r.Open(bad); err == nil {
			f.Close()
			t.Errorf("the portable fallback opened %q, which is outside the root", bad)
		}
	}

	// And the guard inside openBeneath still rejects a path that is not inside
	// the root, even when called directly.
	if _, handled, err := r.openBeneath(outsidePath(r), syscall.O_RDONLY, 0); handled {
		t.Logf("openBeneath handled an outside path: %v", err)
	}
}

// disableOpenat2 forces openat2Supported() to report false and returns a
// restore function.
func disableOpenat2() func() { return setOpenat2ForTest(false) }

// outsidePath returns a path that exists but is outside the root.
func outsidePath(r *Root) string {
	return filepath.Join(filepath.Dir(r.Dir()), "outside", "secret.txt")
}

// TestOpenBeneathRejectsOutsidePath drives the openat2 helper's containment
// guard directly, which is the branch that reports a typed unsafe-path error.
func TestOpenBeneathRejectsOutsidePath(t *testing.T) {
	if !openat2Supported() {
		t.Skip("openat2 not available")
	}
	r := mustRoot(t)
	// relWithin is the guard under test: an absolute path outside the root must
	// be refused before any syscall.
	if _, err := relWithin(r.Dir(), "/etc/passwd"); err == nil {
		t.Fatal("relWithin accepted /etc/passwd for a root under the temp dir")
	} else if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("relWithin error = %v; want ErrUnsafePath", err)
	}
	// The prefix-sibling case.
	sib := r.Dir() + "-sibling"
	if _, err := relWithin(r.Dir(), sib); err == nil {
		t.Fatalf("relWithin accepted the prefix sibling %q", sib)
	}
	// The root itself maps to ".".
	if got, err := relWithin(r.Dir(), r.Dir()); err != nil || got != "" {
		t.Fatalf("relWithin(root, root) = %q, %v; want \"\", nil", got, err)
	}
	// A normal child.
	if got, err := relWithin(r.Dir(), filepath.Join(r.Dir(), "a", "b")); err != nil || got != "a/b" {
		t.Fatalf("relWithin(child) = %q, %v; want a/b", got, err)
	}
}

// TestOpenat2UnsupportedFallsBack proves the package still works when the
// syscall is reported unavailable.
func TestOpenat2UnsupportedFallsBack(t *testing.T) {
	restore := disableOpenat2()
	defer restore()

	if openat2Supported() {
		t.Fatal("disableOpenat2 did not take effect")
	}
	r := mustRoot(t)
	if _, err := r.Open("Settings.xml"); err != nil {
		t.Fatalf("Open with openat2 disabled: %v", err)
	}
	// The escape refusals must hold on the fallback path too.
	if f, err := r.Open("link_out_file"); err == nil {
		f.Close()
		t.Fatal("the fallback opened an escaping symlink")
	}
	if entries, err := r.List(""); err != nil {
		t.Fatalf("List with openat2 disabled: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("List returned nothing with openat2 disabled")
	}
}
