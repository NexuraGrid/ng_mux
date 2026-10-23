//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/ipc"
)

// dialCommands are the client commands that dial an existing server.
func dialCommands(ep ipc.Endpoint) map[string]func() error {
	return map[string]func() error{
		"ls":           func() error { return cmdList(ep) },
		"attach":       func() error { return cmdAttach(ep, nil) },
		"kill-session": func() error { return cmdKillSession(ep, []string{"-t", "x"}) },
		"kill-server":  func() error { return cmdKillServer(ep) },
	}
}

// TestDialCommandsShowTheRealError: a Dial failure other than "nobody is
// listening" (here an unsafe socket dir) must reach the user as is, not as
// "no server running".
func TestDialCommandsShowTheRealError(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	dir := filepath.Join(base, "ngmux-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(nestGuardEnv, "")
	for name, run := range dialCommands(ipc.Endpoint{Name: "dialerr"}) {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
			t.Errorf("%s: err = %v, want the unsafe socket dir error", name, err)
		}
	}
}

// TestDialCommandsReportNoServer keeps the old message when nothing listens.
func TestDialCommandsReportNoServer(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv(nestGuardEnv, "")
	for name, run := range dialCommands(ipc.Endpoint{Name: "dialnone"}) {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "no server running") {
			t.Errorf("%s: err = %v, want no server running", name, err)
		}
	}
}
