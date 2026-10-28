package server

import (
	"strings"
	"sync"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/layout"
)

// TestDefaultShellArgsReachPaneFactory checks that the configured shell and
// its arguments are handed to the pane factory as one argv, both for the first
// pane and for panes added later by a split.
func TestDefaultShellArgsReachPaneFactory(t *testing.T) {
	cases := []struct {
		name  string
		shell string
		args  []string
		want  string // argv joined by "|"; "" = platform default (nil argv)
	}{
		{"with args", "pwsh", []string{"-NoLogo"}, "pwsh|-NoLogo"},
		{"no args", "/bin/bash", nil, "/bin/bash"},
		{"platform default", "", []string{"ignored"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ff := &fakeFleet{}
			inner := ff.factory()
			var (
				mu   sync.Mutex
				seen []string
			)
			factory := func(id layout.PaneID, cols, rows int, shell []string, hist int) (*pane, error) {
				mu.Lock()
				seen = append(seen, strings.Join(shell, "|"))
				mu.Unlock()
				return inner(id, cols, rows, shell, hist)
			}
			sess, err := newSession("work", 80, 24, sessionOpts{
				historyLimit:     100,
				defaultShell:     tc.shell,
				defaultShellArgs: tc.args,
				newPane:          factory,
			}, func(string) {})
			if err != nil {
				t.Fatalf("newSession: %v", err)
			}
			t.Cleanup(sess.shutdown)

			if err := sess.withWindow(func(w *window, cols, rows int) error {
				return w.split(sess, layout.Vertical, cols, rows)
			}); err != nil {
				t.Fatalf("split: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 2 {
				t.Fatalf("factory called %d times, want 2", len(seen))
			}
			for i, got := range seen {
				if got != tc.want {
					t.Errorf("pane %d argv = %q, want %q", i+1, got, tc.want)
				}
			}
		})
	}
}
