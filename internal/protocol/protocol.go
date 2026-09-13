// Package protocol defines the wire format between the ngmux client and server.
//
// Frames are length-prefixed JSON: a 4-byte big-endian uint32 length header
// followed by that many bytes of JSON-encoded Message. This is deliberately
// simple and portable; it works identically over Unix sockets and Windows
// named pipes.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Type identifies the kind of a Message.
type Type string

const (
	// Client -> server.
	TypeAttach      Type = "attach"       // client asks to attach; Name = session, Cols/Rows = size
	TypeInput       Type = "input"        // raw keystrokes for the active pane; carries Data
	TypeResize      Type = "resize"       // client terminal resized; carries Cols/Rows
	TypeCommand     Type = "command"      // a fixed multiplexer command; carries Name (and maybe N)
	TypeExec        Type = "exec"         // a free-form command line; carries Name
	TypeMouse       Type = "mouse"        // a mouse event; MX/MY = cell, MB = button, Name = kind
	TypeRefresh     Type = "refresh"      // ask the server for a full repaint
	TypeListReq     Type = "list_req"     // request the session list
	TypeKillServer  Type = "kill_server"  // shut the server down
	TypeKillSession Type = "kill_session" // kill one session; carries Name

	// Server -> client.
	TypeFrame        Type = "frame"         // pre-rendered ANSI screen update; carries Data
	TypeListReply    Type = "list_reply"    // carries Sessions
	TypeExecReply    Type = "exec_reply"    // result of a TypeExec; Name = output text
	TypeError        Type = "error"         // carries Name (message text)
	TypeBye          Type = "bye"           // server is detaching or closing this client
	TypeSetClipboard Type = "set_clipboard" // Data is an OSC 52 sequence for the client to emit
)

// Mouse event kinds carried in a TypeMouse message's Name.
const (
	MousePress     = "press"
	MouseRelease   = "release"
	MouseDrag      = "drag"
	MouseWheelUp   = "wheel-up"
	MouseWheelDown = "wheel-down"
)

// Command names carried by a TypeCommand message.
const (
	CmdSplitHorizontal = "split-horizontal" // new pane to the right
	CmdSplitVertical   = "split-vertical"   // new pane below
	CmdFocusNext       = "focus-next"
	CmdFocusPrev       = "focus-prev"
	CmdKillPane        = "kill-pane"
	CmdDetach          = "detach"

	// Pane resize; the direction is the edge the active pane's boundary moves.
	CmdResizeLeft  = "resize-left"
	CmdResizeRight = "resize-right"
	CmdResizeUp    = "resize-up"
	CmdResizeDown  = "resize-down"

	// Windows (within the client's current session).
	CmdNewWindow    = "new-window"
	CmdNextWindow   = "next-window"
	CmdPrevWindow   = "prev-window"
	CmdSelectWindow = "select-window" // target index carried in Message.N
	CmdKillWindow   = "kill-window"

	// Sessions (changes which session this client views).
	CmdNextSession = "next-session"
	CmdPrevSession = "prev-session"

	// Copy-mode / paste buffer.
	CmdCopyMode = "copy-mode" // enter scrollback / selection mode on the active pane
	CmdPaste    = "paste"     // write the paste buffer into the active pane
)

// SessionInfo is a single row in a list reply.
type SessionInfo struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Panes    int    `json:"panes"`
	Attached bool   `json:"attached"`
}

// Message is the single envelope exchanged in both directions. Unused fields
// are omitted from the JSON encoding.
type Message struct {
	Type     Type          `json:"t"`
	Cols     int           `json:"cols,omitempty"`
	Rows     int           `json:"rows,omitempty"`
	N        int           `json:"n,omitempty"` // command argument (e.g. select-window index)
	MX       int           `json:"mx,omitempty"`
	MY       int           `json:"my,omitempty"`
	MB       int           `json:"mb,omitempty"`
	Data     []byte        `json:"data,omitempty"`
	Name     string        `json:"name,omitempty"`
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// maxFrame bounds a single message so a corrupt or hostile length header
// cannot make the reader allocate without limit.
const maxFrame = 8 << 20 // 8 MiB

// Conn wraps a byte stream with message framing. Write is safe to call
// concurrently from multiple goroutines: each call serializes header and
// payload into one buffer under a lock and issues a single underlying
// Write, so two callers can never interleave a header from one message
// with the payload of another. Read is not safe for concurrent use — the
// wire is a single ordered stream, so a Conn must have at most one reader
// goroutine at a time.
type Conn struct {
	rwc io.ReadWriteCloser

	writeMu  sync.Mutex
	writeBuf []byte // scratch buffer reused across Write calls, guarded by writeMu
}

// NewConn wraps rwc with message framing.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	return &Conn{rwc: rwc}
}

// Write encodes and sends a single message. Safe for concurrent use; see
// the Conn doc comment.
func (c *Conn) Write(m Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("protocol: marshal: %w", err)
	}
	if len(payload) > maxFrame {
		return fmt.Errorf("protocol: message too large (%d bytes)", len(payload))
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	need := 4 + len(payload)
	if cap(c.writeBuf) < need {
		c.writeBuf = make([]byte, need)
	}
	buf := c.writeBuf[:need]
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)

	_, err = c.rwc.Write(buf)
	return err
}

// Read blocks for the next message. Not safe for concurrent use: callers
// that need to read from multiple goroutines must serialize through a
// single reader.
func (c *Conn) Read() (Message, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.rwc, hdr[:]); err != nil {
		return Message{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return Message{}, fmt.Errorf("protocol: incoming frame too large (%d bytes)", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.rwc, buf); err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(buf, &m); err != nil {
		return Message{}, fmt.Errorf("protocol: unmarshal: %w", err)
	}
	return m, nil
}

// Close closes the underlying stream.
func (c *Conn) Close() error { return c.rwc.Close() }
