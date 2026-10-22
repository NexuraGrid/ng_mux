//go:build !windows

package ipc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// runtimeDir points XDG_RUNTIME_DIR at a fresh temp dir and returns the
// per-user socket dir socketDir is expected to use under it.
func runtimeDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	return filepath.Join(base, "ngmux-"+strconv.Itoa(os.Getuid()))
}

func TestSocketDirCreatesPrivateDir(t *testing.T) {
	want := runtimeDir(t)
	got, err := socketDir()
	if err != nil {
		t.Fatalf("socketDir: %v", err)
	}
	if got != want {
		t.Fatalf("socketDir = %q, want %q", got, want)
	}
	info, err := os.Lstat(got)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want dir 0700", info.Mode())
	}
}

func TestSocketDirAcceptsExistingPrivateDir(t *testing.T) {
	dir := runtimeDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := socketDir(); err != nil {
		t.Fatalf("socketDir on an existing 0700 dir: %v", err)
	}
}

func TestSocketDirRefusesLooseMode(t *testing.T) {
	dir := runtimeDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := socketDir()
	if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
		t.Fatalf("socketDir on a 0755 dir: err = %v, want unsafe permissions", err)
	}
	// Refusing must not "fix" the dir behind the owner's back.
	if info, _ := os.Lstat(dir); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
}

func TestSocketDirRefusesSymlink(t *testing.T) {
	dir := runtimeDir(t)
	target := t.TempDir() // 0700, owned by us: only the symlink is wrong
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	_, err := socketDir()
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("socketDir on a symlink: err = %v, want not a directory", err)
	}
}

func TestListenAndDialRefuseUnsafeDir(t *testing.T) {
	dir := runtimeDir(t)
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	ep := Endpoint{Name: "unsafe"}
	if l, err := Listen(ep); err == nil {
		l.Close()
		t.Fatal("Listen succeeded in a world-writable socket dir")
	}
	if _, err := Dial(ep); err == nil || err == ErrNoServer {
		t.Fatalf("Dial: err = %v, want the unsafe-dir error (not ErrNoServer)", err)
	}
}

func TestListenDialRoundTrip(t *testing.T) {
	runtimeDir(t)
	ep := Endpoint{Name: "roundtrip"}
	l, err := Listen(ep)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
		}
	}()
	c, err := Dial(ep)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	c.Close()
}

// A umask (or a default POSIX ACL on the base dir) can strip bits from the
// dir socketDir creates; a dir we just made is ours and must end up 0700.
func TestSocketDirNormalizesFreshDirMode(t *testing.T) {
	want := runtimeDir(t)
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	got, err := socketDir()
	if err != nil {
		t.Fatalf("socketDir under umask 0277: %v", err)
	}
	if info, _ := os.Lstat(want); got != want || info.Mode().Perm() != 0o700 {
		t.Fatalf("socketDir = %q mode %v, want %q 0700", got, info.Mode().Perm(), want)
	}
}

// TestSocketDirAcceptsRacingFreshDir is B19: another ngmux started at the same
// moment under umask 0277 has just created the dir (mode 0500) but not yet
// chmod'ed it. The dir is ours and owner-only, so it must be accepted and
// brought to 0700, not refused as unsafe.
func TestSocketDirAcceptsRacingFreshDir(t *testing.T) {
	for _, mode := range []os.FileMode{0o500, 0o300, 0o000} {
		dir := runtimeDir(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := socketDir(); err != nil {
			t.Fatalf("socketDir on our own %#o dir: %v", mode, err)
		}
		if info, _ := os.Lstat(dir); info.Mode().Perm() != 0o700 {
			t.Fatalf("%#o dir: mode = %v, want 0700", mode, info.Mode().Perm())
		}
	}
}

// TestSocketDirRefusesGroupBit: an owner-only check must not accept a dir
// that grants the group anything, even with the other bits clear.
func TestSocketDirRefusesGroupBit(t *testing.T) {
	dir := runtimeDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o710); err != nil {
		t.Fatal(err)
	}
	_, err := socketDir()
	if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
		t.Fatalf("socketDir on a 0710 dir: err = %v, want unsafe permissions", err)
	}
	if info, _ := os.Lstat(dir); info.Mode().Perm() != 0o710 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
}
