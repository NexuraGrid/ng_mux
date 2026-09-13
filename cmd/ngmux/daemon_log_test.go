package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDaemonLogPath(t *testing.T) {
	tests := []struct {
		name   string
		epName string
		want   string
	}{
		{"plain name", "default", "ngmux/ngmuxd-default.log"},
		{"numeric session name", "0", "ngmux/ngmuxd-0.log"},
		{"empty name falls back to default", "", "ngmux/ngmuxd-default.log"},
		{"path separators are sanitized", "a/b", "ngmux/ngmuxd-a_b.log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daemonLogPath("/cache", tt.epName)
			want := filepath.Join("/cache", tt.want)
			if got != want {
				t.Fatalf("daemonLogPath(%q) = %q, want %q", tt.epName, got, want)
			}
		})
	}
}

func TestSanitizeLogName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "default"},
		{"default", "default"},
		{"a/b\\c:d", "a_b_c_d"},
		{"session-1_ok", "session-1_ok"},
	}
	for _, tt := range tests {
		if got := sanitizeLogName(tt.in); got != tt.want {
			t.Errorf("sanitizeLogName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestOpenDaemonLogFileCreatesDirAndFile uses t.TempDir() as the cache root so
// this never touches the real user cache directory.
func TestOpenDaemonLogFileCreatesDirAndFile(t *testing.T) {
	dir := t.TempDir()
	path := daemonLogPath(dir, "test")

	f, err := openDaemonLogFile(path)
	if err != nil {
		t.Fatalf("openDaemonLogFile: %v", err)
	}
	defer f.Close()

	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Permission bits are not meaningfully portable to Windows ACLs.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir perm = %o, want 0700", perm)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat file: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("file perm = %o, want 0600", perm)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("file content = %q, want it to contain %q", data, "hello")
	}
}

// TestOpenDaemonLogFileTruncatesOversizedLog is the regression test for the
// 5 MiB startup rotation: a log that has already grown past the threshold
// must be truncated rather than appended to forever.
func TestOpenDaemonLogFileTruncatesOversizedLog(t *testing.T) {
	dir := t.TempDir()
	path := daemonLogPath(dir, "test")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	oversized := make([]byte, maxDaemonLogSize+1)
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}

	f, err := openDaemonLogFile(path)
	if err != nil {
		t.Fatalf("openDaemonLogFile: %v", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("size after opening an oversized log = %d, want 0 (truncated)", info.Size())
	}

	if _, err := f.WriteString("fresh start\n"); err != nil {
		t.Fatalf("write after truncation: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "fresh start\n" {
		t.Fatalf("content after truncation = %q, want only the new write", data)
	}
}

// TestOpenDaemonLogFileKeepsSmallLog asserts a log under the threshold is
// appended to, not clobbered, across daemon restarts.
func TestOpenDaemonLogFileKeepsSmallLog(t *testing.T) {
	dir := t.TempDir()
	path := daemonLogPath(dir, "test")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	f, err := openDaemonLogFile(path)
	if err != nil {
		t.Fatalf("openDaemonLogFile: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("more\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "existing\nmore\n" {
		t.Fatalf("content = %q, want existing content preserved and appended to", data)
	}
}
