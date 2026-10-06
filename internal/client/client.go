// Package client is the thin half of ngmux. It connects to the daemon, puts
// the real terminal into raw mode, forwards keystrokes (intercepting the
// prefix key for multiplexer commands), and writes the ANSI frames the server
// sends straight to stdout.
package client

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/config"
	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/termio"
	"github.com/MauricioJC3/ng_mux/internal/theme"
)

// inReader is the client's stdin, delivered one byte at a time over a channel
// so a read can be given a deadline. A bare Esc is ambiguous — it could be the
// start of an arrow-key sequence or the user pressing Escape — and blocking for
// the next byte forever (as a plain bufio.Reader does) makes apps that care
// about a lone Esc misbehave while attached. ReadByteTimeout waits only
// escape-time for the rest of a sequence, then releases the Esc on its own.
type inReader struct {
	ch   chan byte
	back int // one byte of pushback, or -1

	stop     chan struct{} // closed by close() to release the reader goroutine
	stopOnce sync.Once

	mu  sync.Mutex
	err error
}

func newInReader(r io.Reader) *inReader {
	ir := &inReader{ch: make(chan byte, 4096), back: -1, stop: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			for _, b := range buf[:n] {
				select {
				case ir.ch <- b:
				case <-ir.stop:
					return
				}
			}
			if err != nil {
				ir.mu.Lock()
				ir.err = err
				ir.mu.Unlock()
				close(ir.ch)
				return
			}
		}
	}()
	return ir
}

// close releases anything blocked on the reader: the pump goroutine stuck on a
// full channel, and a ReadByte waiting for the next key. Safe to call more than
// once. It does not unblock an in-progress os.Stdin read; that ends with the
// process.
func (ir *inReader) close() { ir.stopOnce.Do(func() { close(ir.stop) }) }

func (ir *inReader) readErr() error {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	if ir.err != nil {
		return ir.err
	}
	return io.EOF
}

// ReadByte blocks for the next byte, or returns the stream's end error.
func (ir *inReader) ReadByte() (byte, error) {
	if ir.back >= 0 {
		b := byte(ir.back)
		ir.back = -1
		return b, nil
	}
	select {
	case b, ok := <-ir.ch:
		if !ok {
			return 0, ir.readErr()
		}
		return b, nil
	case <-ir.stop:
		return 0, ir.readErr()
	}
}

// pushBack returns one byte to the stream for the next ReadByte. Only the most
// recent byte is kept; the input loop never needs more than one.
func (ir *inReader) pushBack(b byte) { ir.back = int(b) }

// Buffered reports how many bytes can be read without blocking.
func (ir *inReader) Buffered() int {
	n := len(ir.ch)
	if ir.back >= 0 {
		n++
	}
	return n
}

// ReadByteTimeout returns the next byte, or ok=false if none arrives within d
// (or the stream ends). Already-buffered bytes are returned without waiting, so
// an escape sequence the terminal delivered in one burst is never split by the
// deadline. d <= 0 means "do not wait at all".
func (ir *inReader) ReadByteTimeout(d time.Duration) (b byte, ok bool) {
	if ir.back >= 0 {
		b, ir.back = byte(ir.back), -1
		return b, true
	}
	select {
	case b, chOK := <-ir.ch:
		return b, chOK
	default:
	}
	if d <= 0 {
		return 0, false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case b, chOK := <-ir.ch:
		return b, chOK
	case <-timer.C:
		return 0, false
	case <-ir.stop:
		return 0, false
	}
}

// lockedWriter serializes writes to the terminal so the frame stream and the
// local command prompt never interleave mid-sequence.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) WriteString(s string) { _, _ = l.Write([]byte(s)) }

// mouseOn / mouseOff toggle SGR mouse reporting (button + drag + wheel).
const (
	mouseOn  = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
)

// DefaultPrefix is Ctrl-b, matching tmux. The config file can override it.
const DefaultPrefix = config.DefaultPrefix

// keymap is the resolved prefix key plus any user bindings from the config.
type keymap struct {
	prefix byte
	binds  map[string]string // single-rune key -> command name
}

// enterAlt / exitAlt switch the host terminal to its alternate screen so the
// user's shell scrollback is untouched while attached.
const (
	enterAlt = "\x1b[?1049h"
	exitAlt  = "\x1b[?1049l\x1b[0m"
)

// Attach connects to the daemon at ep, attaches to the named session (empty
// means the server's default), and runs until the session detaches, the server
// exits, or stdin closes.
func Attach(ep ipc.Endpoint, session string, in, out *os.File) error {
	conn, err := ipc.Dial(ep)
	if err != nil {
		return err
	}
	pc := protocol.NewConn(conn)

	size, err := termio.GetSize(out)
	if err != nil || size.Cols == 0 {
		size = termio.Size{Cols: 80, Rows: 24}
	}
	if err := pc.Write(protocol.Message{
		Type: protocol.TypeAttach,
		Name: session,
		Cols: size.Cols,
		Rows: size.Rows,
	}); err != nil {
		return err
	}

	cfg, _ := config.Load()
	km := keymap{prefix: cfg.Prefix, binds: cfg.Binds}
	if km.prefix == 0 {
		km.prefix = DefaultPrefix
	}

	sess, err := termio.Enter(in, out)
	if err != nil {
		return err
	}
	defer sess.Restore()

	w := &lockedWriter{w: out}
	w.WriteString(enterAlt)
	defer w.WriteString(exitAlt)
	if cfg.Mouse {
		w.WriteString(mouseOn)
		defer w.WriteString(mouseOff)
	}

	escapeDelay := time.Duration(cfg.EscapeTime) * time.Millisecond

	// overlay is set while a local popup (the Ctrl-b m cheat-sheet) owns the
	// screen, so readFrames holds back server frames that would paint over it.
	var overlay atomic.Bool

	stopResize := make(chan struct{})
	defer close(stopResize)
	go termio.WatchResize(out, func(s termio.Size) {
		pc.Write(protocol.Message{Type: protocol.TypeResize, Cols: s.Cols, Rows: s.Rows})
	}, stopResize)

	// Input goroutine: stdin -> server. Ends when stdin errors or, on teardown,
	// when ir.close() releases it.
	ir := newInReader(in)
	defer ir.close()
	inputErr := make(chan error, 1)
	pal := cfg.Palette()
	go func() { inputErr <- forwardInput(ir, w, out, pc, km, &pal, escapeDelay, &overlay) }()

	// Main goroutine: server -> stdout, until Bye or disconnect.
	readErr := readFrames(pc, w, &overlay)

	select {
	case <-inputErr:
	default:
	}
	if errors.Is(readErr, errDetached) || errors.Is(readErr, io.EOF) {
		return nil
	}
	return readErr
}

var errDetached = errors.New("detached")

// Exec sends a one-shot command line to the daemon and prints its reply. It
// does not attach. Used by the `ngmux <command>` CLI form.
func Exec(ep ipc.Endpoint, line string) error {
	conn, err := ipc.Dial(ep)
	if err != nil {
		return err
	}
	defer conn.Close()
	pc := protocol.NewConn(conn)
	if err := pc.Write(protocol.Message{Type: protocol.TypeExec, Name: line}); err != nil {
		return err
	}
	reply, err := pc.Read()
	if err != nil {
		return err
	}
	switch reply.Type {
	case protocol.TypeExecReply:
		if reply.Name != "" {
			io.WriteString(os.Stdout, reply.Name+"\n")
		}
		return nil
	case protocol.TypeError:
		return errors.New(reply.Name)
	default:
		return nil
	}
}

// readFrames pumps server messages to the terminal until the session ends.
// While overlay is set a local popup owns the screen, so screen-painting
// messages are dropped; the input loop sends a Refresh when it clears the
// overlay, so the next frame is a clean full repaint.
func readFrames(pc *protocol.Conn, out io.Writer, overlay *atomic.Bool) error {
	for {
		msg, err := pc.Read()
		if err != nil {
			return err
		}
		switch msg.Type {
		case protocol.TypeFrame:
			if overlay.Load() {
				continue
			}
			if _, err := out.Write(msg.Data); err != nil {
				return err
			}
		case protocol.TypeSetClipboard:
			// An OSC 52 sequence: no visible output, so it is safe to write
			// even while the which-key overlay is up.
			if _, err := out.Write(msg.Data); err != nil {
				return err
			}
		case protocol.TypeExecReply:
			if msg.Name != "" && !overlay.Load() {
				out.Write([]byte("\x1b[999;1H\x1b[2K" + firstLine(msg.Name)))
			}
		case protocol.TypeBye:
			return errDetached
		case protocol.TypeError:
			return errors.New("server: " + msg.Name)
		}
	}
}

func firstLine(s string) string {
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// forwardInput reads raw keystrokes and routes them: escape sequences (arrows,
// mouse) via handleEscape, the prefix key into command handling (including the
// ':' command prompt), everything else straight to the focused pane. term is
// the real terminal, used only to size the prefix cheat-sheet popup. escapeDelay
// is how long a lone Esc waits for the rest of a sequence before being sent.
func forwardInput(br *inReader, out *lockedWriter, term *os.File, pc *protocol.Conn, km keymap, pal *theme.Palette, escapeDelay time.Duration, overlay *atomic.Bool) error {
	prefix := km.prefix
	for {
		b, err := br.ReadByte()
		if err != nil {
			return err
		}

		switch {
		case b == 0x1b:
			if err := handleEscape(br, pc, escapeDelay); err != nil {
				return err
			}

		case b == prefix:
			// Show the cheat-sheet while we block for the next key, unless the
			// user already typed it (a buffered byte) — then skip the flash.
			hide, shown := func() {}, false
			if br.Buffered() == 0 {
				hide, shown = showWhichKey(out, term, km, pal), true
			}
			cmd, err := br.ReadByte()
			if err != nil {
				hide()
				return err
			}
			hide()
			// Repaint over where the panel was. The Ctrl-b m popup owns the
			// screen itself and refreshes on its own after. A prompt waits for
			// this repaint to land before drawing on the bottom row.
			settle := time.Duration(0)
			if shown && cmd != 'm' {
				pc.Write(protocol.Message{Type: protocol.TypeRefresh})
				settle = promptSettle
			}
			quick, isQuick := quickPrompts[cmd]
			switch {
			case cmd == ':':
				commandPrompt(br, out, pc, linePrompt, settle)
			case cmd == prefix:
				// prefix twice: send one literal prefix byte to the pane.
				pc.Write(protocol.Message{Type: protocol.TypeInput, Data: []byte{prefix}})
			case isQuick && km.binds[string(rune(cmd))] == "":
				commandPrompt(br, out, pc, quick, settle)
			case cmd == 0x1b:
				if line, ok := arrowCommand(br); ok {
					pc.Write(protocol.Message{Type: protocol.TypeExec, Name: line})
				}
			case cmd == 'm' && km.binds["m"] == "":
				// A local cheat-sheet: how to create, list and move between
				// sessions. Any key dismisses it. overlay keeps server frames
				// from painting over it while it is up.
				overlay.Store(true)
				hideHelp := showSessionHelp(out, term, pal)
				_, _ = br.ReadByte()
				hideHelp()
				overlay.Store(false)
				pc.Write(protocol.Message{Type: protocol.TypeRefresh})
			default:
				line, ok := km.resolveKey(cmd)
				if !ok {
					continue
				}
				pc.Write(protocol.Message{Type: protocol.TypeExec, Name: line})
				if line == cmdDetach {
					return errDetached
				}
			}

		default:
			buf := []byte{b}
			for br.Buffered() > 0 {
				nb, e := br.ReadByte()
				if e != nil {
					break
				}
				if nb == prefix || nb == 0x1b {
					br.pushBack(nb)
					break
				}
				buf = append(buf, nb)
			}
			if err := pc.Write(protocol.Message{Type: protocol.TypeInput, Data: buf}); err != nil {
				return err
			}
		}
	}
}

// handleEscape consumes an escape sequence (0x1b already read). SGR mouse
// sequences become TypeMouse; anything else is forwarded verbatim as input. A
// lone Esc with nothing behind it within escapeDelay is forwarded on its own
// rather than held until the next keystroke.
func handleEscape(br *inReader, pc *protocol.Conn, escapeDelay time.Duration) error {
	b1, ok := br.ReadByteTimeout(escapeDelay)
	if !ok {
		return pc.Write(protocol.Message{Type: protocol.TypeInput, Data: []byte{0x1b}})
	}
	if b1 != '[' {
		return pc.Write(protocol.Message{Type: protocol.TypeInput, Data: []byte{0x1b, b1}})
	}
	b2, err := br.ReadByte()
	if err != nil {
		return pc.Write(protocol.Message{Type: protocol.TypeInput, Data: []byte{0x1b, '['}})
	}
	if b2 == '<' { // SGR mouse: ESC [ < Cb ; Cx ; Cy (M|m)
		var body []byte
		for {
			c, e := br.ReadByte()
			if e != nil {
				return nil
			}
			if c == 'M' || c == 'm' {
				body = append(body, c)
				break
			}
			body = append(body, c)
		}
		if msg, ok := parseSGRMouse(body); ok {
			return pc.Write(msg)
		}
		return nil
	}
	// Any other CSI: forward ESC [ b2 … up to and including the final byte.
	seq := []byte{0x1b, '[', b2}
	for !isCSIFinal(b2) {
		c, e := br.ReadByte()
		if e != nil {
			break
		}
		seq = append(seq, c)
		b2 = c
	}
	return pc.Write(protocol.Message{Type: protocol.TypeInput, Data: seq})
}

func isCSIFinal(b byte) bool { return b >= 0x40 && b <= 0x7e }

// parseSGRMouse decodes the "Cb;Cx;Cy(M|m)" body of an SGR mouse report.
func parseSGRMouse(body []byte) (protocol.Message, bool) {
	if len(body) < 2 {
		return protocol.Message{}, false
	}
	final := body[len(body)-1]
	fields := bytes.Split(body[:len(body)-1], []byte{';'})
	if len(fields) != 3 {
		return protocol.Message{}, false
	}
	cb, err1 := strconv.Atoi(string(fields[0]))
	cx, err2 := strconv.Atoi(string(fields[1]))
	cy, err3 := strconv.Atoi(string(fields[2]))
	if err1 != nil || err2 != nil || err3 != nil {
		return protocol.Message{}, false
	}
	var kind string
	switch {
	case cb&64 != 0:
		if cb&1 != 0 {
			kind = protocol.MouseWheelDown
		} else {
			kind = protocol.MouseWheelUp
		}
	case cb&32 != 0:
		kind = protocol.MouseDrag
	case final == 'm':
		kind = protocol.MouseRelease
	default:
		kind = protocol.MousePress
	}
	return protocol.Message{
		Type: protocol.TypeMouse, Name: kind,
		MX: cx - 1, MY: cy - 1, MB: cb & 3,
	}, true
}

// promptSpec is one flavour of the bottom-row line editor: the label drawn in
// front of what the user types, and how the typed text becomes the command
// line sent to the server. An empty command line sends nothing.
type promptSpec struct {
	label string
	build func(input string) string
}

// linePrompt is the ':' command prompt: what is typed is the command line.
var linePrompt = promptSpec{label: ":", build: func(s string) string { return s }}

// namePrompt asks for a name and runs "cmd NAME"; a blank answer does nothing.
func namePrompt(label, cmd string) promptSpec {
	return promptSpec{label: label, build: func(s string) string {
		if name := strings.TrimSpace(s); name != "" {
			return cmd + " " + name
		}
		return ""
	}}
}

// quickPrompts are the prefix keys that open the prompt already aimed at one
// command, as in tmux: the user types only the argument. A config `bind` of the
// same key wins over these.
var quickPrompts = map[byte]promptSpec{
	',': namePrompt("rename window: ", "rename-window"),
	'$': namePrompt("rename session: ", "rename-session"),
}

// promptSettle is how long a prompt opened right after the which-key panel
// waits before drawing, so the Refresh that repaints behind the panel does not
// paint over it.
const promptSettle = 60 * time.Millisecond

// commandPrompt runs a line editor locally, drawing spec's label and the typed
// text on the bottom row. Enter sends spec.build of the text (if non-empty),
// Esc cancels. settle delays the first draw (see promptSettle).
func commandPrompt(br *inReader, out *lockedWriter, pc *protocol.Conn, spec promptSpec, settle time.Duration) {
	var buf []byte
	draw := func() { out.WriteString("\x1b[?25h\x1b[999;1H\x1b[2K" + spec.label + string(buf)) }
	if settle > 0 {
		time.Sleep(settle)
	}
	draw()
	for {
		b, err := br.ReadByte()
		if err != nil {
			return
		}
		switch b {
		case '\r', '\n':
			if line := spec.build(string(buf)); line != "" {
				pc.Write(protocol.Message{Type: protocol.TypeExec, Name: line})
			}
			pc.Write(protocol.Message{Type: protocol.TypeRefresh})
			return
		case 0x1b: // Esc: cancel (swallow a following arrow-key body if any)
			for br.Buffered() > 0 {
				if nb, _ := br.ReadByte(); isCSIFinal(nb) {
					break
				}
			}
			pc.Write(protocol.Message{Type: protocol.TypeRefresh})
			return
		case 0x7f, 0x08:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
			draw()
		default:
			if b >= 0x20 && b < 0x7f {
				buf = append(buf, b)
				draw()
			}
		}
	}
}

// cmdDetach is the one command line the input loop reacts to locally: after
// sending it the client stops reading input and lets Attach return.
const cmdDetach = "detach-client"

// defaultKeyCommands maps the key pressed after the prefix to the command line
// sent to the server. Digits 0-9 (select-window) and the arrow keys are handled
// separately. Config `bind` directives override entries here.
var defaultKeyCommands = map[byte]string{
	'"': "split-window -v", // top / bottom
	'%': "split-window -h", // left / right
	'o': "select-pane",     // next pane
	';': "previous-pane",
	'x': "kill-pane",
	'c': "new-window",
	'n': "next-window",
	'p': "previous-window",
	'&': "kill-window",
	'(': "previous-session",
	')': "next-session",
	'[': "copy-mode",
	']': "paste-buffer",
	'z': "resize-pane -Z", // toggle full-screen zoom for the active pane
	'q': "display-panes",  // briefly show each pane's index
	'{': "swap-pane -U",   // swap the active pane with the previous one
	'}': "swap-pane -D",   // swap the active pane with the next one
	'!': "break-pane",     // move the active pane into its own window
	'H': "resize-pane -L",
	'J': "resize-pane -D",
	'K': "resize-pane -U",
	'L': "resize-pane -R",
	'd': cmdDetach,
}

// legacyBindCommands translates the short command names older ngmux.conf files
// use in `bind` directives into current command lines. A bind value that is not
// listed here is passed through unchanged, so a new config can bind a full
// command line directly (e.g. `bind S split-window -v`).
var legacyBindCommands = map[string]string{
	"split-vertical":   "split-window -v",
	"split-horizontal": "split-window -h",
	"focus-next":       "select-pane",
	"focus-prev":       "previous-pane",
	"focus-previous":   "previous-pane",
	"paste":            "paste-buffer",
	"detach":           cmdDetach,
	"prev-window":      "previous-window",
	"prev-session":     "previous-session",
}

// resolveKey maps the byte after the prefix to a server command line. ok is
// false when the key is unbound.
func (km keymap) resolveKey(key byte) (line string, ok bool) {
	if b, bound := km.binds[string(rune(key))]; bound {
		if mapped, isLegacy := legacyBindCommands[b]; isLegacy {
			return mapped, true
		}
		return b, true
	}
	if key >= '0' && key <= '9' {
		return "select-window " + string(rune(key)), true
	}
	if line, has := defaultKeyCommands[key]; has {
		return line, true
	}
	return "", false
}

// arrowCommand reads the "[A|B|C|D" body of an arrow key pressed after the
// prefix (the leading ESC is already consumed) and maps it to a focus move.
func arrowCommand(br io.ByteReader) (string, bool) {
	if b1, err := br.ReadByte(); err != nil || b1 != '[' {
		return "", false
	}
	b2, err := br.ReadByte()
	if err != nil {
		return "", false
	}
	switch b2 {
	case 'A', 'D':
		return "previous-pane", true
	case 'B', 'C':
		return "select-pane", true
	}
	return "", false
}
