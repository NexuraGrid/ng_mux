package client

import "testing"

func TestJoinArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		// Plain arguments pass through as before.
		{[]string{"select-layout", "tiled"}, "select-layout tiled"},
		{[]string{"send-keys", "-t", "0", "ls", "Enter"}, "send-keys -t 0 ls Enter"},
		// Whitespace, quotes and empty arguments are single-quoted.
		{[]string{"send-keys", "echo a b", "Enter"}, "send-keys 'echo a b' Enter"},
		{[]string{"rename-window", "it's"}, `rename-window 'it'"'"'s'`},
		{[]string{"send-keys", `say "hi"`}, `send-keys 'say "hi"'`},
		{[]string{"rename-window", ""}, "rename-window ''"},
	} {
		if got := JoinArgs(tc.args); got != tc.want {
			t.Errorf("JoinArgs(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
