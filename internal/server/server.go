// Package server is the ngmux daemon. It owns every session, window, pane, pty
// and emulator, renders frames, and streams ANSI updates to attached clients
// over the ipc transport. Clients are thin: they send keystrokes and blit bytes.
package server

import (
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/config"
	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// frameInterval is how often the daemon repaints. ~33 fps is smooth for a
// terminal and cheap: a frame with no changes produces zero bytes on the wire.
const frameInterval = 30 * time.Millisecond

// defaultSessionName is used when a client attaches without naming a session.
const defaultSessionName = "0"

// Server is a running daemon instance.
type Server struct {
	ep  ipc.Endpoint
	ln  net.Listener
	log *log.Logger

	initCols, initRows int

	opts sessionOpts

	mu       sync.Mutex
	sessions map[string]*session
	order    []string
	clients  map[*client]struct{}

	quitOnce sync.Once
	done     chan struct{}

	// lastMinute is the wall-clock minute the broadcaster last painted, so the
	// status-bar clock still advances even when nothing else changed. -1 forces
	// the first tick to paint.
	lastMinute int
}

// newServer builds a Server with no listener attached. Splitting construction
// from I/O lets tests drive sessions and commands without binding a socket.
func newServer(ep ipc.Endpoint, initCols, initRows int, logger *log.Logger, opts sessionOpts) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	// Every session/window/pane created under opts logs through the same
	// daemon logger, so a recovered panic anywhere is visible in the same
	// place regardless of which pane or connection it came from.
	opts.logf = logger.Printf
	return &Server{
		ep:         ep,
		log:        logger,
		initCols:   initCols,
		initRows:   initRows,
		opts:       opts,
		sessions:   make(map[string]*session),
		clients:    make(map[*client]struct{}),
		done:       make(chan struct{}),
		lastMinute: -1,
	}
}

// Run starts a daemon on ep and blocks until it shuts down (last session
// emptied, or a client sent kill-server). initCols/initRows seed the first
// session's size before any client attaches.
func Run(ep ipc.Endpoint, initCols, initRows int, logger *log.Logger) error {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		logger.Printf("config: %v (using defaults)", cfgErr)
	}
	for _, w := range cfg.Warnings {
		logger.Printf("config: %s", w)
	}

	srv := newServer(ep, initCols, initRows, logger, sessionOpts{
		historyLimit: cfg.HistoryLimit,
		defaultShell: cfg.DefaultShell,
		statusFG:     cfg.StatusFG,
		statusBG:     cfg.StatusBG,
		setClipboard: cfg.SetClipboard,
	})

	ln, err := ipc.Listen(ep)
	if err != nil {
		return err
	}
	srv.ln = ln

	// Start with one session so `ngmux new` has something to attach to.
	if _, err := srv.getOrCreateSession(defaultSessionName); err != nil {
		ln.Close()
		ipc.Remove(ep)
		return err
	}

	go srv.acceptLoop()
	go srv.broadcastLoop()

	<-srv.done
	srv.ln.Close()
	ipc.Remove(ep)
	srv.shutdownAll()
	srv.log.Printf("server on %q stopped", ep.Name)
	return nil
}

func (s *Server) quit() {
	s.quitOnce.Do(func() { close(s.done) })
}

// getOrCreateSession returns the named session, creating it if absent.
func (s *Server) getOrCreateSession(name string) (*session, error) {
	if name == "" {
		name = defaultSessionName
	}
	s.mu.Lock()
	if sess, ok := s.sessions[name]; ok {
		s.mu.Unlock()
		return sess, nil
	}
	s.mu.Unlock()

	sess, err := newSession(name, s.initCols, s.initRows, s.opts, s.sessionEmptied)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if existing, ok := s.sessions[name]; ok { // lost a race; keep the first
		s.mu.Unlock()
		sess.shutdown()
		return existing, nil
	}
	s.sessions[name] = sess
	s.order = append(s.order, name)
	s.mu.Unlock()
	return sess, nil
}

// nextSessionName returns the lowest non-negative integer name that is not
// already taken, matching tmux's naming for sessions created without a name.
func (s *Server) nextSessionName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; ; i++ {
		name := strconv.Itoa(i)
		if _, taken := s.sessions[name]; !taken {
			return name
		}
	}
}

// hasSession reports whether a session by that name currently exists.
func (s *Server) hasSession(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[name]
	return ok
}

// sessionEmptied is invoked by a session when its last window closes. It drops
// the session and, under the same lock, moves any client that was viewing it to
// a surviving session so a burst of sessions emptying together can never leave a
// client pointed at a name that is already gone.
func (s *Server) sessionEmptied(name string) {
	s.mu.Lock()
	if sess, ok := s.sessions[name]; ok {
		delete(s.sessions, name)
		for i, n := range s.order {
			if n == name {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		sess.shutdown()
	}

	// Pick a surviving session to move orphaned clients onto. Walk s.order
	// rather than trusting s.order[0] so a stale entry can't be handed out.
	fallback := ""
	for _, n := range s.order {
		if _, ok := s.sessions[n]; ok {
			fallback = n
			break
		}
	}
	if fallback == "" {
		s.mu.Unlock()
		s.quit()
		return
	}

	for c := range s.clients {
		if c.session() == name {
			c.setSession(fallback)
			c.reset()
		}
	}
	s.mu.Unlock()
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Printf("accept: %v", err)
			return
		}
		go s.handleConn(conn)
	}
}

// handleConn is spawned per accepted connection by acceptLoop. The deferred
// recover covers this whole goroutine, including serveClient (called
// synchronously below for an attach): a panic anywhere in one client's
// request handling must never take every other session and client down with
// it. Cleanup on a recovered panic is best-effort — the per-branch and
// serveClient defers (removeClient -> client.close -> pc.Close) run as usual
// when the panic originates inside them, but a panic before any of those run
// can leak this one connection, which is a far smaller problem than crashing
// the daemon.
func (s *Server) handleConn(conn net.Conn) {
	defer recoverAndLog(s.log.Printf, "handleConn")
	pc := protocol.NewConn(conn)
	first, err := pc.Read()
	if err != nil {
		conn.Close()
		return
	}

	switch first.Type {
	case protocol.TypeListReq:
		_ = pc.Write(protocol.Message{Type: protocol.TypeListReply, Sessions: s.listSessions()})
		conn.Close()

	case protocol.TypeKillServer:
		_ = pc.Write(protocol.Message{Type: protocol.TypeBye})
		conn.Close()
		s.quit()

	case protocol.TypeKillSession:
		s.killSession(first.Name)
		_ = pc.Write(protocol.Message{Type: protocol.TypeBye})
		conn.Close()

	case protocol.TypeExec:
		out, err := s.execCommand(nil, first.Name)
		if err != nil {
			_ = pc.Write(protocol.Message{Type: protocol.TypeError, Name: err.Error()})
		} else {
			_ = pc.Write(protocol.Message{Type: protocol.TypeExecReply, Name: out})
		}
		conn.Close()

	case protocol.TypeAttach:
		s.serveClient(pc, first)

	default:
		_ = pc.Write(protocol.Message{Type: protocol.TypeError, Name: "expected attach as first message"})
		conn.Close()
	}
}

func (s *Server) listSessions() []protocol.SessionInfo {
	s.mu.Lock()
	names := append([]string(nil), s.order...)
	sessions := make(map[string]*session, len(s.sessions))
	for k, v := range s.sessions {
		sessions[k] = v
	}
	attached := make(map[string]bool)
	for c := range s.clients {
		attached[c.session()] = true
	}
	s.mu.Unlock()

	out := make([]protocol.SessionInfo, 0, len(names))
	for _, n := range names {
		if sess := sessions[n]; sess != nil {
			out = append(out, sess.info(attached[n]))
		}
	}
	return out
}

func (s *Server) killSession(name string) {
	s.mu.Lock()
	sess := s.sessions[name]
	s.mu.Unlock()
	if sess != nil {
		sess.shutdown()
		s.sessionEmptied(name)
	}
}

// serveClient runs one attached client until it detaches or disconnects.
func (s *Server) serveClient(pc *protocol.Conn, attach protocol.Message) {
	sess, err := s.getOrCreateSession(attach.Name)
	if err != nil {
		_ = pc.Write(protocol.Message{Type: protocol.TypeError, Name: err.Error()})
		pc.Close()
		return
	}
	if attach.Cols > 0 && attach.Rows > 0 {
		sess.resize(attach.Cols, attach.Rows)
	}

	cl := newClient(pc, sess.name)
	cl.setSize(attach.Cols, attach.Rows)
	s.addClient(cl)
	defer s.removeClient(cl)

	go cl.writeLoop()
	cl.reset()

	for {
		msg, err := pc.Read()
		if err != nil {
			return
		}
		cur := s.sessionByName(cl.session())
		switch msg.Type {
		case protocol.TypeInput:
			if cur != nil {
				repaint, yank := cur.input(msg.Data)
				if yank != "" && s.opts.setClipboard {
					if seq := osc52(yank); seq != nil {
						cl.send(protocol.Message{Type: protocol.TypeSetClipboard, Data: seq})
					}
				}
				if repaint {
					s.markDirty(cur)
					cl.reset()
				}
			}
		case protocol.TypeResize:
			cl.setSize(msg.Cols, msg.Rows)
			if cur != nil {
				cur.resize(msg.Cols, msg.Rows)
				s.markDirty(cur)
			}
			cl.reset()
		case protocol.TypeRefresh:
			cl.reset()
		case protocol.TypeMouse:
			if cur != nil && cur.mouse(msg.Name, msg.MX, msg.MY, msg.MB) {
				s.markDirty(cur)
				cl.reset()
			}
		case protocol.TypeExec:
			out, err := s.execCommand(cl, msg.Name)
			if err != nil {
				cl.send(protocol.Message{Type: protocol.TypeExecReply, Name: "error: " + err.Error()})
			} else if out != "" {
				cl.send(protocol.Message{Type: protocol.TypeExecReply, Name: out})
			}
		case protocol.TypeCommand:
			// Legacy fixed-command path. CmdDetach still gets an immediate
			// teardown; everything else is translated to a registry command
			// line and run through the one dispatch path.
			if msg.Name == protocol.CmdDetach {
				cl.send(protocol.Message{Type: protocol.TypeBye})
				return
			}
			if line := commandLine(msg.Name, msg.N); line != "" {
				if _, err := s.execCommand(cl, line); err != nil {
					s.log.Printf("command %q: %v", msg.Name, err)
				}
			}
		case protocol.TypeKillServer:
			cl.send(protocol.Message{Type: protocol.TypeBye})
			s.quit()
			return
		}
	}
}

func (s *Server) switchClientSession(cl *client, forward bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) < 2 {
		return
	}
	idx := 0
	for i, n := range s.order {
		if n == cl.session() {
			idx = i
			break
		}
	}
	if forward {
		idx = (idx + 1) % len(s.order)
	} else {
		idx = (idx - 1 + len(s.order)) % len(s.order)
	}
	cl.setSession(s.order[idx])
}

func (s *Server) sessionByName(name string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[name]
}

// broadcastLoop repaints on every tick, but only for sessions that actually
// changed: a session with no pane output and no pending needsRepaint is skipped
// entirely, so an idle daemon does no rendering work. The status-bar clock is
// kept live by repainting once when the wall-clock minute rolls over.
func (s *Server) broadcastLoop() {
	t := time.NewTicker(frameInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick renders and sends one frame to every attached client. It is split out
// of broadcastLoop, with its own deferred recover, so a panic while composing
// or sending a frame (a corrupted snapshot, a bug in render.Paint) is logged
// and skipped for this tick instead of killing the ticker goroutine outright,
// which would freeze every attached client's screen with no diagnostic ever
// printed.
func (s *Server) tick() {
	defer recoverAndLog(s.log.Printf, "broadcastLoop tick")

	clients, sessions, ok := s.tickSnapshot()
	if !ok {
		return
	}

	nowMin := time.Now().Minute()
	clockTick := nowMin != s.lastMinute
	s.lastMinute = nowMin

	// Group clients by the session they are viewing so each session's frame
	// is composed at most once per tick.
	bySession := make(map[string][]*client, len(sessions))
	for _, c := range clients {
		name := c.session()
		bySession[name] = append(bySession[name], c)
	}

	for name, viewers := range bySession {
		sess := sessions[name]
		if sess == nil {
			continue
		}
		need := clockTick || sess.dirty()
		if !need {
			for _, c := range viewers {
				if c.needsFrame() { // fresh attach / reset / resize, or skipped while busy
					need = true
					break
				}
			}
		}
		if !need {
			continue
		}

		frame := sess.frame()
		for _, c := range viewers {
			c.offerFrame(frame)
		}
	}
}

// tickSnapshot copies out the client list and session map under s.mu, using a
// defer so the lock is released even if a panic happens mid-copy. ok is false
// when there is nothing to do this tick (no attached clients).
func (s *Server) tickSnapshot() (clients []*client, sessions map[string]*session, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) == 0 {
		return nil, nil, false
	}
	clients = make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	sessions = make(map[string]*session, len(s.sessions))
	for k, v := range s.sessions {
		sessions[k] = v
	}
	return clients, sessions, true
}

// markDirty flags a session so the broadcaster rebuilds its frame on the next
// tick even if no pane produced output (focus/layout/name changes, resizes,
// copy-mode navigation).
func (s *Server) markDirty(sess *session) {
	if sess == nil {
		return
	}
	sess.mu.Lock()
	sess.needsRepaint = true
	sess.mu.Unlock()
}

// resetAllClients forces every attached client's next frame to be a full
// repaint. Used after a CLI-issued command that could change any session.
func (s *Server) resetAllClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.reset()
	}
}

func (s *Server) addClient(c *client) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeClient(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	// Let a bye queued just before returning (detach, kill-server) reach the
	// wire before the connection closes.
	c.closeAfterFlush(flushTimeout)
}

func (s *Server) shutdownAll() {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = make(map[string]*session)
	s.order = nil
	clients := s.clients
	s.clients = make(map[*client]struct{})
	s.mu.Unlock()

	for _, sess := range sessions {
		sess.shutdown()
	}
	for c := range clients {
		c.close()
	}
}
