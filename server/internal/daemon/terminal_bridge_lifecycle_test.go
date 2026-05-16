package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/terminal"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// openBridgeSession is the shared "open one terminal session through the
// bridge" helper for lifecycle tests: spawns a fake PTY, opens it via the
// bridge, waits for terminal.opened to come back, and returns the session
// id plus the fake PTY (so the test can push child output later).
func openBridgeSession(t *testing.T, bridge *terminalBridge, sender *captureSender, pty *fakeBridgePTY) string {
	t.Helper()
	openPayload, err := json.Marshal(protocol.TerminalOpenPayload{
		RequestID:   "req-bp",
		TaskID:      "task-bp",
		WorkspaceID: "ws-bp",
		WorkDir:     t.TempDir(),
		Cols:        80,
		Rows:        24,
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	bridge.handleFrame(protocol.MessageTypeTerminalOpen, openPayload)
	openedMsg := sender.waitFor(t, protocol.MessageTypeTerminalOpened, time.Second)
	var opened protocol.TerminalOpenedPayload
	if err := json.Unmarshal(openedMsg.Payload, &opened); err != nil {
		t.Fatalf("opened payload: %v", err)
	}
	if opened.SessionID == "" {
		t.Fatalf("expected non-empty session id")
	}
	return opened.SessionID
}

// TestTerminalBridge_DataBackpressureNoSilentDrop pins Phase 2 review
// blocker 2: terminal.data must NOT be silently dropped when the daemon's
// outbound WS queue is saturated. Instead, the pump back-pressures the
// PTY reader via a blocking send (with ctx escape), so the eventual
// reader still sees every byte.
//
// The shape of the test:
//
//   - We use a writes channel of size 1 to mimic a hot, saturated hub.
//   - sendCtx blocks on this channel (the real backpressure path).
//   - The test pushes 4 PTY chunks into the session while the consumer
//     is asleep — the pump cannot drop them.
//   - The consumer then drains all 4 frames in order.
//
// If the bridge regresses to the old `default: drop` behavior, fewer
// than 4 chunks will be observed and the assertion fails.
func TestTerminalBridge_DataBackpressureNoSilentDrop(t *testing.T) {
	writes := make(chan []byte, 1)
	sendCtx := func(ctx context.Context, frame []byte) bool {
		select {
		case writes <- frame:
			return true
		case <-ctx.Done():
			return false
		}
	}

	pty := newFakeBridgePTY(80, 24)
	spawner := &stubSpawner{pty: pty}
	mgr := terminal.NewManager(terminal.ManagerConfig{
		Spawner: spawner,
		Logger:  slog.Default(),
	}, nil)
	defer mgr.Close()

	sender := &captureSender{}
	bridge := newTerminalBridge(mgr, slog.Default(), sender.send, sendCtx)

	sessionID := openBridgeSession(t, bridge, sender, pty)

	// Push 4 chunks. The pump can only buffer 1 in the writes channel; the
	// remainder must back-pressure via the PTY's bounded output channel
	// (cap 4 in fakeBridgePTY). If any chunk were dropped instead of
	// pressed back, the count below would fall short.
	chunks := []string{"chunk-1\n", "chunk-2\n", "chunk-3\n", "chunk-4\n"}
	for _, c := range chunks {
		pty.out <- []byte(c)
	}

	// Drain writes; reassemble the data frames the pump emitted.
	got := make([]string, 0, len(chunks))
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < len(chunks) {
		select {
		case frame := <-writes:
			var env protocol.Message
			if err := json.Unmarshal(frame, &env); err != nil {
				t.Fatalf("envelope: %v", err)
			}
			if env.Type != protocol.MessageTypeTerminalData {
				continue
			}
			var dp protocol.TerminalDataPayload
			if err := json.Unmarshal(env.Payload, &dp); err != nil {
				t.Fatalf("data payload: %v", err)
			}
			if dp.SessionID != sessionID {
				t.Fatalf("session_id mismatch: got %q want %q", dp.SessionID, sessionID)
			}
			decoded, err := base64.StdEncoding.DecodeString(dp.DataB64)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got = append(got, string(decoded))
		case <-time.After(time.Until(deadline)):
			t.Fatalf("only saw %d/%d chunks before timeout — backpressure regressed to silent drop", len(got), len(chunks))
		}
	}

	for i, c := range chunks {
		if got[i] != c {
			t.Errorf("chunk %d: got %q, want %q", i, got[i], c)
		}
	}
}

// TestTerminalBridge_OpenedEnqueueFailureClosesSession pins Phase 4
// Round 2 finding 2: the terminal.opened handshake frame must NOT be
// treated as a droppable control frame. If the daemon WS writer queue is
// saturated at the moment we'd ack a fresh PTY back to the server proxy,
// the daemon must roll the half-open session back instead of leaving an
// orphaned PTY that the proxy will never address (the proxy gives up
// after its 5s open timeout without a session_id, so it can't even send
// terminal.close).
//
// The shape of the test:
//
//   - send returns false (saturated writer) for terminal.opened.
//   - The bridge handles terminal.open: it spawns the PTY, tries to ack,
//     observes the drop, and tears the session down.
//   - We assert that the manager has zero live sessions after the bridge
//     processes the open — i.e., the orphaned-session leak is gone.
//
// Before the fix, manager.Sessions() would still report 1 live session
// (and pinned active-env mark with it) and the only thing that would
// eventually clean it up was the idle sweep.
func TestTerminalBridge_OpenedEnqueueFailureClosesSession(t *testing.T) {
	// Sender that ALWAYS reports drop on the non-blocking send path. This
	// is the worst-case version of "writer queue is full and we can't
	// enqueue another control frame right now" — the previous code would
	// silently log and move on, leaving the session hanging.
	dropSend := func(_ []byte) bool { return false }
	// sendCtx is irrelevant here (no PTY data is going to flow), but the
	// bridge insists on a non-nil fn; pass through to dropSend so the
	// shape matches production.
	dropSendCtx := func(_ context.Context, _ []byte) bool { return false }

	pty := newFakeBridgePTY(80, 24)
	spawner := &stubSpawner{pty: pty}
	mgr := terminal.NewManager(terminal.ManagerConfig{
		Spawner: spawner,
		Logger:  slog.Default(),
	}, nil)
	defer mgr.Close()

	bridge := newTerminalBridge(mgr, slog.Default(), dropSend, dropSendCtx)

	openPayload, err := json.Marshal(protocol.TerminalOpenPayload{
		RequestID:   "req-drop",
		TaskID:      "task-drop",
		WorkspaceID: "ws-drop",
		WorkDir:     t.TempDir(),
		Cols:        80,
		Rows:        24,
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}

	bridge.handleFrame(protocol.MessageTypeTerminalOpen, openPayload)

	// Spawn definitely happened — the bug we are guarding against is
	// "spawn succeeded, ack dropped, session orphaned".
	if spawner.callCount() != 1 {
		t.Fatalf("spawner.callCount = %d, want 1 (open path should still spawn the PTY before attempting the ack)", spawner.callCount())
	}

	// The session must be torn down within a tight deadline. Manager
	// reports an empty Sessions() list once waitLoop has finalized the
	// closure triggered by sess.Close("opened_enqueue_failed").
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mgr.Sessions()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := mgr.Sessions(); len(got) != 0 {
		t.Fatalf("manager still has %d session(s) after dropped terminal.opened — orphan regressed", len(got))
	}

	// Bridge's internal route map must be empty too — otherwise a later
	// terminal.close or closeAll would dereference a stale route.
	bridge.mu.Lock()
	routes := len(bridge.sessions)
	bridge.mu.Unlock()
	if routes != 0 {
		t.Fatalf("bridge.sessions still has %d route(s) — half-open rollback didn't unlink", routes)
	}
}

// TestTerminalBridge_TeardownDoesNotPanicOnInFlightSend pins Phase 2
// review blocker 1: when the daemonws connection drops while a terminal
// pump is mid-send, the teardown must NOT cause `send on closed channel`.
// The required invariant is that bridge.closeAll cancels and *waits for*
// every pump goroutine before the wakeup loop closes the writes channel.
//
// This test models the wakeup loop's teardown sequence directly:
//
//  1. Wire a writes channel and a backpressure sendCtx, same as production.
//  2. Open a session and stall the pump on a full writes channel.
//  3. Run closeAll → close(writes) in the same goroutine, exactly the
//     order wakeup.go now uses.
//  4. Assert: no panic, teardown completes within a tight deadline.
//
// Before the fix, closeAll returned while the pump was still inside its
// blocking send, and the subsequent close(writes) would panic the pump
// the moment select picked the closed channel.
func TestTerminalBridge_TeardownDoesNotPanicOnInFlightSend(t *testing.T) {
	writes := make(chan []byte, 1)
	sendCtx := func(ctx context.Context, frame []byte) bool {
		select {
		case writes <- frame:
			return true
		case <-ctx.Done():
			return false
		}
	}

	pty := newFakeBridgePTY(80, 24)
	spawner := &stubSpawner{pty: pty}
	mgr := terminal.NewManager(terminal.ManagerConfig{
		Spawner: spawner,
		Logger:  slog.Default(),
	}, nil)
	defer mgr.Close()

	sender := &captureSender{}
	bridge := newTerminalBridge(mgr, slog.Default(), sender.send, sendCtx)

	_ = openBridgeSession(t, bridge, sender, pty)

	// Push two chunks so the pump has one in writes (queued) and one
	// blocked on the next select. That blocked send is the exact race
	// window the old defer order tripped over.
	pty.out <- []byte("first\n")
	pty.out <- []byte("second\n")

	// Give the pump a moment to actually park on the blocking send.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("teardown panicked: %v", r)
			}
			close(done)
		}()
		// Mirror wakeup.go's folded cleanup defer:
		//   clearWSWrites equivalent → bridge.closeAll → close(writes)
		bridge.closeAll("ws_disconnect")
		close(writes)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown did not finish — closeAll likely did not wait for pump exit")
	}

	// Drain any residual frames so the writes channel close is observable
	// and the test doesn't leak goroutines.
	for range writes {
	}
}
