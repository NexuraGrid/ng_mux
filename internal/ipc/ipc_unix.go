//go:build !windows

package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// socketDir is the per-user directory that holds server sockets. It prefers
// XDG_RUNTIME_DIR (already user-private and tmpfs) and falls back to /tmp.
//
// Under a shared /tmp another user can create ngmux-<uid> first, so an
// existing dir is trusted only if it is a real directory (not a symlink),
// owned by us and with no group or other permission bits; anything else is
// refused, as tmux does, instead of listening or dialing through a dir someone
// else controls. A trusted dir is brought to 0700.
func socketDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "ngmux-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("ipc: socket dir %s is not a directory", dir)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("ipc: socket dir %s has unsafe permissions (want owner uid %d, no group or other access)", dir, os.Getuid())
	}
	// Ours and owner-only, but maybe narrower than 0700: a restrictive umask
	// (0277 gives 0500), possibly from another ngmux that created it a moment
	// ago and has not chmod'ed it yet. Nobody else can have touched it, so
	// bring it to 0700 instead of refusing it.
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func socketPath(e Endpoint) (string, error) {
	dir, err := socketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, e.Name), nil
}

// Listen creates the server socket, removing a stale one left by a crash.
func Listen(e Endpoint) (net.Listener, error) {
	path, err := socketPath(e)
	if err != nil {
		return nil, err
	}
	// If something is already listening, this endpoint is taken.
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("ipc: server already running at %s", path)
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Dial connects to a running server. It returns ErrNoServer if nothing is
// listening at the endpoint.
func Dial(e Endpoint) (net.Conn, error) {
	path, err := socketPath(e)
	if err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return nil, ErrNoServer
		}
		// Socket file exists but nobody answered: stale.
		return nil, ErrNoServer
	}
	return c, nil
}

// Remove deletes the socket file for an endpoint (used on clean shutdown).
func Remove(e Endpoint) {
	if path, err := socketPath(e); err == nil {
		_ = os.Remove(path)
	}
}
