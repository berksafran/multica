package terminal

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// ExitInfo describes how a session terminated.
type ExitInfo struct {
	ExitCode int
	Reason   string
}

// PtySession is a single live PTY + child shell. Methods are safe for
// concurrent use; readers consume from Output() and ExitC() until
// Output() is closed, which always follows an ExitC() send.
type PtySession struct {
	id          string
	taskID      string
	workspaceID string
	issueID     string
	workDir     string
	userID      string
	shellPath   string

	mu          sync.Mutex
	cols, rows  uint16
	pty         PTY
	output      chan []byte
	exit        chan ExitInfo
	done        chan struct{}
	stop        chan struct{}
	stopOnce    sync.Once
	closing     bool
	closeReason string

	// wg tracks readLoop and idleLoop. waitLoop is the finalizer: it
	// waits on wg before closing output/done so we never close the
	// output channel while readLoop is mid-send.
	wg sync.WaitGroup

	now         func() time.Time
	idleTimeout time.Duration
	startedAt   time.Time
	lastIO      time.Time

	logger  *slog.Logger
	onClose func(string)
}

// ID returns the session identifier.
func (s *PtySession) ID() string { return s.id }

// TaskID returns the task this session is bound to.
func (s *PtySession) TaskID() string { return s.taskID }

// WorkspaceID returns the workspace this session belongs to.
func (s *PtySession) WorkspaceID() string { return s.workspaceID }

// IssueID returns the issue this session was opened from, if any.
func (s *PtySession) IssueID() string { return s.issueID }

// WorkDir returns the cwd of the child shell.
func (s *PtySession) WorkDir() string { return s.workDir }

// UserID returns the human user who opened the session.
func (s *PtySession) UserID() string { return s.userID }

// Shell returns the shell binary path that was spawned.
func (s *PtySession) Shell() string { return s.shellPath }

// StartedAt returns the wall-clock time the session was spawned.
func (s *PtySession) StartedAt() time.Time { return s.startedAt }

// LastIO returns the most recent time data flowed in either direction.
func (s *PtySession) LastIO() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastIO
}

// Output yields PTY output chunks as they arrive. The channel closes
// after the child exits and a value has been delivered on ExitC().
func (s *PtySession) Output() <-chan []byte { return s.output }

// ExitC fires once when the child exits. After that, Output() closes.
func (s *PtySession) ExitC() <-chan ExitInfo { return s.exit }

// Done returns a channel closed when the session is fully torn down
// (all goroutines exited, registry deregistered).
func (s *PtySession) Done() <-chan struct{} { return s.done }

// Write forwards bytes to the PTY stdin. Returns the byte count actually
// written. Updates LastIO so idle detection sees the activity.
func (s *PtySession) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return 0, ErrSessionNotFound
	}
	pty := s.pty
	s.lastIO = s.now()
	s.mu.Unlock()
	return pty.Write(p)
}

// Resize updates the PTY window size.
func (s *PtySession) Resize(cols, rows uint16) error {
	cols, rows = normalizeSize(cols, rows)
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrSessionNotFound
	}
	s.cols = cols
	s.rows = rows
	pty := s.pty
	s.lastIO = s.now()
	s.mu.Unlock()
	return pty.Resize(cols, rows)
}

// Size returns the current cols, rows of the PTY.
func (s *PtySession) Size() (uint16, uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Close tears down the session. Subsequent calls are no-ops. The
// reason is recorded for audit logging and the terminal.exit payload.
//
// Close only initiates teardown — signals stop, closes the PTY, returns.
// waitLoop is the actual finalizer: it waits for readLoop + idleLoop
// to exit (via wg) before closing output/done. That ordering is what
// makes "Close while output buffer is full" safe — readLoop's blocked
// send unblocks on <-stop, and only then does the output channel close.
func (s *PtySession) Close(reason string) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.closing = true
	s.closeReason = reason
	pty := s.pty
	s.mu.Unlock()

	s.stopOnce.Do(func() { close(s.stop) })

	if pty != nil {
		// pty.Close on the unix spawner runs SIGHUP → grace → SIGKILL.
		// It's idempotent (sync.Once), so the second call from waitLoop's
		// finalizer is a no-op.
		_ = pty.Close()
	}
}

// start kicks off the reader, exit-watch, and (optional) idle
// goroutines. Manager.Open is the only caller. wg.Add runs
// synchronously before waitLoop is spawned so wg.Wait sees the
// correct count even if Close fires immediately.
func (s *PtySession) start() {
	s.wg.Add(1)
	go s.readLoop()
	if s.idleTimeout > 0 {
		s.wg.Add(1)
		go s.idleLoop()
	}
	go s.waitLoop()
}

func (s *PtySession) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.mu.Lock()
			s.lastIO = s.now()
			s.mu.Unlock()
			select {
			case s.output <- chunk:
			case <-s.stop:
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && err != io.ErrClosedPipe {
				s.logger.Debug("pty read error", "err", err)
			}
			return
		}
	}
}

func (s *PtySession) waitLoop() {
	code, waitErr := s.pty.Wait()

	s.mu.Lock()
	reason := s.closeReason
	if reason == "" {
		if waitErr != nil {
			reason = "wait_error"
		} else {
			reason = "exited"
		}
		s.closeReason = reason
	}
	s.closing = true
	s.mu.Unlock()

	// Ensure the PTY fd is closed so readLoop's pty.Read returns EOF.
	// pty.Close is idempotent (sync.Once on the unix spawner).
	_ = s.pty.Close()

	// Signal stop so idleLoop and any blocked send in readLoop exit.
	s.stopOnce.Do(func() { close(s.stop) })

	// Wait for readLoop + idleLoop before closing output/done. This is
	// the invariant that prevents "send on closed channel" panics when
	// output is full: readLoop is either past its send or unblocked via
	// <-stop, but never racing with close(s.output).
	s.wg.Wait()

	// Finalize order is load-bearing: external waiters use `<-Done()` as
	// a signal that the session is fully torn down AND deregistered from
	// the manager. The sequence must be:
	//   ExitC → close(output) → onClose/deregister → close(done)
	// so that any consumer doing `<-Done(); manager.Get(id)` after a
	// teardown is guaranteed to observe ErrSessionNotFound.
	select {
	case s.exit <- ExitInfo{ExitCode: code, Reason: reason}:
	default:
	}
	close(s.output)
	if s.onClose != nil {
		s.onClose(s.id)
	}
	close(s.done)
}

func (s *PtySession) idleLoop() {
	defer s.wg.Done()
	// Sample at IdleTimeout/4 so reaction time is bounded but ticks
	// stay cheap with many sessions. Manager.CheckIdle catches anything
	// this loop misses (e.g. when daemon's outer GC tick is coarser).
	interval := s.idleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if s.now().Sub(s.LastIO()) >= s.idleTimeout {
				// Close calls pty.Close + waits for wg in waitLoop. If
				// we ran it inline, waitLoop's wg.Wait would block on
				// this goroutine, which can't exit until Close returns
				// — deadlock. Spawning lets idleLoop return and
				// decrement wg.
				go s.Close("idle_timeout")
				return
			}
		}
	}
}
