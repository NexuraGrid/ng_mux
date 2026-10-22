package server

import (
	"reflect"
	"testing"

	cli "github.com/MauricioJC3/ng_mux/internal/client"
)

// TestTokenizeRoundTripsJoinArgs: whatever shell arguments the CLI quotes with
// client.JoinArgs, the server's tokenizer must split back into the same tokens.
func TestTokenizeRoundTripsJoinArgs(t *testing.T) {
	for _, args := range [][]string{
		{"send-keys", "-t", "0", "echo a b", "Enter"},
		{"rename-window", "it's"},
		{"send-keys", `say "hi" it's`},
		{"rename-window", ""},
		{"send-keys", "tab\there"},
	} {
		if got := tokenize(cli.JoinArgs(args)); !reflect.DeepEqual(got, args) {
			t.Errorf("tokenize(JoinArgs(%q)) = %q", args, got)
		}
	}
}
