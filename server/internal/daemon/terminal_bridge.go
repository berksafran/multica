package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/multica-ai/multica/server/internal/daemon/terminal"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// terminalBridge adapts the daemon-side terminal.Manager to the daemonws
// WebSocket transport. Per session it:
//
//   - relays PtySession.Output() → terminal.data frames (daemon→server)
//   - relays PtySession.ExitC()  → terminal.exit frames
//   - tears the bridge goroutine down when Done() fires
//
// Two send paths are wired:
//
//   - send       (non-blocking): used for control / handshake frames that
//                are safe to drop on backlog (terminal.opened, terminal.exit,
//                terminal.error). Maps to Daemon.sendWSFrame.
//   - sendCtx    (blocking with ctx escape): used for PTY data frames so a
//                saturated hub writer back-pressures the producer instead
//                of corrupting the terminal byte stream. Maps to
//                Daemon.sendWSFrameCtx.
type terminalBridge struct {
	manager *terminal.Manager
	logger  *slog.Logger
	send    func([]byte) bool
	sendCtx func(context.Context, []byte) bool

	mu       sync.Mutex
	sessions map[string]*terminalRoute
}

type terminalRoute struct {
	session  *terminal.PtySession
	cancel   context.CancelFunc
	pumpDone chan struct{}
}

func newTerminalBridge(mgr *terminal.Manager, logger *slog.Logger, send func([]byte) bool, sendCtx func(context.Context, []byte) bool) *terminalBridge {
	return &terminalBridge{
		manager:  mgr,
		logger:   logger,
		send:     send,
		sendCtx:  sendCtx,
		sessions: make(map[string]*terminalRoute),
	}
}

// handleFrame dispatches a single terminal.* envelope from the server. The
// caller already decoded protocol.Message; we receive the inner type+payload.
func (b *terminalBridge) handleFrame(msgType string, payload json.RawMessage) {
	switch msgType {
	case protocol.MessageTypeTerminalOpen:
		var p protocol.TerminalOpenPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			b.logger.Debug("terminal.open invalid payload", "error", err)
			return
		}
		b.handleOpen(p)
	case protocol.MessageTypeTerminalData:
		var p protocol.TerminalDataPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			b.logger.Debug("terminal.data invalid payload", "error", err)
			return
		}
		b.handleData(p)
	case protocol.MessageTypeTerminalResize:
		var p protocol.TerminalResizePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			b.logger.Debug("terminal.resize invalid payload", "error", err)
			return
		}
		b.handleResize(p)
	case protocol.MessageTypeTerminalClose:
		var p protocol.TerminalClosePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			b.logger.Debug("terminal.close invalid payload", "error", err)
			return
		}
		b.handleClose(p)
	}
}

func (b *terminalBridge) handleOpen(p protocol.TerminalOpenPayload) {
	info := terminal.TaskInfo{
		TaskID:         p.TaskID,
		WorkspaceID:    p.WorkspaceID,
		IssueID:        p.IssueID,
		WorkDir:        p.WorkDir,
		PriorSessionID: p.PriorSessionID,
	}
	sess, err := b.manager.OpenWithInfo(context.Background(), info, terminal.OpenParams{
		TaskID:      p.TaskID,
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
		Cols:        p.Cols,
		Rows:        p.Rows,
	})
	if err != nil {
		b.sendError(p.RequestID, "", mapTerminalError(err), err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	pumpDone := make(chan struct{})
	b.mu.Lock()
	b.sessions[sess.ID()] = &terminalRoute{session: sess, cancel: cancel, pumpDone: pumpDone}
	b.mu.Unlock()

	// terminal.opened is the handshake frame — the server proxy stays in the
	// "no session_id yet" state until it sees us, so dropping it on a full
	// writer would orphan the freshly spawned PTY: the proxy hits its 5s
	// open timeout and closes itself with no session_id to send
	// terminal.close on, leaving the daemon-side session alive until the
	// idle sweep (and pinning the GC active-env mark for that whole window).
	// We must NOT treat this frame as a droppable control frame.
	if !b.sendFrame(protocol.MessageTypeTerminalOpened, protocol.TerminalOpenedPayload{
		RequestID: p.RequestID,
		SessionID: sess.ID(),
		WorkDir:   sess.WorkDir(),
		Shell:     sess.Shell(),
	}) {
		// Roll the half-open session back: cancel the pump context (no pump
		// has been started yet, but be explicit so the contract holds if a
		// future change starts the pump before this branch), drop the route
		// from the map, and close the session so waitLoop tears the PTY
		// down. The server proxy will surface the failure via its own open
		// timeout.
		cancel()
		close(pumpDone)
		b.mu.Lock()
		delete(b.sessions, sess.ID())
		b.mu.Unlock()
		sess.Close("opened_enqueue_failed")
		return
	}

	go func() {
		defer close(pumpDone)
		b.pump(ctx, sess)
	}()
}

func (b *terminalBridge) handleData(p protocol.TerminalDataPayload) {
	sess, err := b.manager.Get(p.SessionID)
	if err != nil {
		b.sendError("", p.SessionID, protocol.TerminalErrorCodeSessionNotFound, err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(p.DataB64)
	if err != nil {
		b.logger.Debug("terminal.data invalid base64", "error", err, "session_id", p.SessionID)
		return
	}
	if _, err := sess.Write(data); err != nil {
		b.logger.Debug("terminal.data write failed", "error", err, "session_id", p.SessionID)
	}
}

func (b *terminalBridge) handleResize(p protocol.TerminalResizePayload) {
	sess, err := b.manager.Get(p.SessionID)
	if err != nil {
		b.sendError("", p.SessionID, protocol.TerminalErrorCodeSessionNotFound, err.Error())
		return
	}
	if err := sess.Resize(p.Cols, p.Rows); err != nil {
		b.logger.Debug("terminal.resize failed", "error", err, "session_id", p.SessionID)
	}
}

func (b *terminalBridge) handleClose(p protocol.TerminalClosePayload) {
	sess, err := b.manager.Get(p.SessionID)
	if err != nil {
		// Already gone — nothing to do; the server side has already received
		// a terminal.exit frame (or will, through the pump goroutine).
		return
	}
	reason := p.Reason
	if reason == "" {
		reason = "client_close"
	}
	sess.Close(reason)
}

// pump bridges one session's output channel onto the WS as terminal.data
// frames, and emits a terminal.exit when the child exits. Returns when
// either the session is fully torn down or ctx is cancelled.
//
// terminal.data is delivered with REAL backpressure (sendDataFrame blocks
// on a full hub writer). That is intentional: a saturated writer must
// slow the PTY reader down, not drop bytes — half-streams break shells
// far worse than a momentary lag. Heartbeat / control frames still go
// through the droppable send path because they are recoverable.
func (b *terminalBridge) pump(ctx context.Context, sess *terminal.PtySession) {
	sessionID := sess.ID()
	defer func() {
		b.mu.Lock()
		delete(b.sessions, sessionID)
		b.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-sess.Output():
			if !ok {
				// Output closed → child exited and waitLoop finalized.
				// ExitC was already delivered (or about to be); pull it once
				// non-blocking and forward, then exit the pump.
				var info terminal.ExitInfo
				select {
				case info = <-sess.ExitC():
				default:
				}
				b.sendFrame(protocol.MessageTypeTerminalExit, protocol.TerminalExitPayload{
					SessionID: sessionID,
					ExitCode:  info.ExitCode,
					Reason:    info.Reason,
				})
				<-sess.Done()
				return
			}
			if !b.sendDataFrame(ctx, sessionID, chunk) {
				// ctx canceled (bridge being torn down) — bail. We don't
				// emit a terminal.exit here: the teardown path on the
				// caller side already accounts for the session going away.
				return
			}
		}
	}
}

// sendDataFrame is the backpressure-aware variant of sendFrame, used only
// for terminal.data. Returns false iff ctx was canceled mid-send (i.e.,
// the bridge is being torn down).
func (b *terminalBridge) sendDataFrame(ctx context.Context, sessionID string, chunk []byte) bool {
	raw, err := json.Marshal(protocol.TerminalDataPayload{
		SessionID: sessionID,
		DataB64:   base64.StdEncoding.EncodeToString(chunk),
	})
	if err != nil {
		b.logger.Debug("terminal data payload marshal failed", "error", err, "session_id", sessionID)
		return true
	}
	frame, err := json.Marshal(protocol.Message{Type: protocol.MessageTypeTerminalData, Payload: raw})
	if err != nil {
		b.logger.Debug("terminal data envelope marshal failed", "error", err, "session_id", sessionID)
		return true
	}
	if b.sendCtx == nil {
		// Defensive: pre-test bridges may not have plumbed sendCtx. Fall
		// back to the non-blocking sender so existing tests still run.
		_ = b.send(frame)
		return true
	}
	return b.sendCtx(ctx, frame)
}

// closeAll tears down every live session. Called when the daemon
// disconnects from the server: the browser proxy will fail downstream,
// and a reconnect cannot resurrect the pre-existing PTYs because the
// session_ids only existed in the prior WS context.
//
// closeAll BLOCKS until every pump goroutine has actually exited. The
// wakeup loop relies on this guarantee: after closeAll returns, no pump
// goroutine can still be calling sendWSFrameCtx, so the wakeup loop can
// safely close the writes channel without racing producers.
func (b *terminalBridge) closeAll(reason string) {
	b.mu.Lock()
	routes := make([]*terminalRoute, 0, len(b.sessions))
	for _, r := range b.sessions {
		routes = append(routes, r)
	}
	b.mu.Unlock()
	for _, r := range routes {
		r.cancel()
		r.session.Close(reason)
	}
	for _, r := range routes {
		if r.pumpDone != nil {
			<-r.pumpDone
		}
	}
}

// sendFrame marshals and enqueues a single terminal.* frame on the
// non-blocking send path. Returns true iff the frame made it into the
// writer queue. Callers MUST check the return value when the frame is
// part of an irreversible handshake (terminal.opened) — see handleOpen
// for the rollback path.
func (b *terminalBridge) sendFrame(msgType string, payload any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		b.logger.Debug("terminal frame marshal failed", "error", err, "type", msgType)
		return false
	}
	frame, err := json.Marshal(protocol.Message{Type: msgType, Payload: raw})
	if err != nil {
		b.logger.Debug("terminal envelope marshal failed", "error", err, "type", msgType)
		return false
	}
	if !b.send(frame) {
		b.logger.Debug("terminal frame dropped: ws disconnected or backed up", "type", msgType)
		return false
	}
	return true
}

func (b *terminalBridge) sendError(requestID, sessionID, code, message string) {
	b.sendFrame(protocol.MessageTypeTerminalError, protocol.TerminalErrorPayload{
		RequestID: requestID,
		SessionID: sessionID,
		Code:      code,
		Message:   message,
	})
}

// mapTerminalError translates the terminal package's sentinel errors into
// protocol error codes the browser proxy can render. Anything we don't
// recognise falls back to TerminalErrorCodeInternal — drop information
// rather than surface internal wrap text to the user.
func mapTerminalError(err error) string {
	switch {
	case errors.Is(err, terminal.ErrWorkspaceMismatch):
		return protocol.TerminalErrorCodeWorkspaceMismatch
	case errors.Is(err, terminal.ErrTaskNotFound):
		return protocol.TerminalErrorCodeTaskNotFound
	case errors.Is(err, terminal.ErrSessionNotFound):
		return protocol.TerminalErrorCodeSessionNotFound
	case errors.Is(err, terminal.ErrUnsupportedOS):
		return protocol.TerminalErrorCodeUnsupportedOS
	case errors.Is(err, terminal.ErrSpawnFailed):
		return protocol.TerminalErrorCodeSpawnFailed
	}
	return protocol.TerminalErrorCodeInternal
}
