package server

import "runtime/debug"

// recoverAndLog recovers a panic in the calling goroutine and logs it via
// logf (nil-safe: a caller with no logger configured just drops the message
// rather than panicking itself), then lets the goroutine continue normally.
//
// It must be deferred directly — `defer recoverAndLog(logf, "where")` — never
// wrapped in another function, because recover() only stops a panic when
// called by the deferred function itself.
//
// This is the daemon's last line of defense against a goroutine panic taking
// the whole process down with it: one malformed escape sequence, one bad
// connection, or one bug anywhere below the defer point must never be able to
// kill every other session, pane and client. It is deliberately dumb (no
// retries, no state repair) — callers that need to keep working after a
// recovered panic (e.g. Term.Write's own emulator-panic recovery) handle that
// themselves; this only stops the bleeding at the goroutine boundary.
func recoverAndLog(logf func(format string, args ...any), where string) {
	if r := recover(); r != nil {
		if logf != nil {
			logf("recovered panic in %s: %v\n%s", where, r, debug.Stack())
		}
	}
}
