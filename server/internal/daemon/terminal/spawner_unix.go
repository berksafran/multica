//go:build !windows

package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// closeGracePeriod is the window between SIGHUP and SIGKILL during a
// Close. Long enough for interactive shells to run trap handlers and
// flush state; short enough that closing a tab feels instant.
const closeGracePeriod = 250 * time.Millisecond

// realSpawner forks the shell on a PTY using creack/pty. Linux/macOS
// only; Windows reaches the stub in spawner_windows.go and returns
// ErrUnsupportedOS.
type realSpawner struct{}

func (realSpawner) Start(req SpawnRequest) (PTY, error) {
	cmd := exec.Command(req.Shell, req.Args...)
	cmd.Dir = req.Cwd

	// Inherit the daemon's PATH so users get whatever CLIs are installed
	// in the daemon's environment (claude, codex, multica, etc.); merge
	// in the per-session vars built by buildEnv.
	env := os.Environ()
	env = append(env, req.Env...)
	cmd.Env = env

	size := &pty.Winsize{Cols: req.Cols, Rows: req.Rows}
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}
	return &unixPTY{cmd: cmd, file: f}, nil
}

type unixPTY struct {
	cmd       *exec.Cmd
	file      *os.File
	exited    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.file.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.file.Write(b) }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.file, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *unixPTY) Wait() (int, error) {
	err := p.cmd.Wait()
	p.exited.Store(true)
	if p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode(), err
	}
	return -1, err
}

// Close terminates the child shell and releases the PTY master fd.
// Closing a tab is a hangup, not an interrupt — so the signal path is
// SIGHUP → brief grace → SIGKILL → file.Close, in that order:
//
//   - SIGHUP gives interactive shells a chance to run trap handlers,
//     write history, etc. before the fd disappears.
//   - The grace window is bounded; anything slower than that is stuck.
//   - SIGKILL is the cliff for shells that ignore HUP.
//   - file.Close releases the master fd last so the slave side keeps
//     working during cleanup.
//
// Signals are sent to the negated pid so they hit the whole process
// group. creack/pty starts the child as a session leader (Setsid), so
// pid == pgid and any descendants the user spawned in the shell are
// caught by the same kill.
//
// If the child already exited naturally (Wait returned), all signal
// work is skipped — we only close the fd. That avoids a pointless
// 250ms sleep in the natural-exit teardown path.
func (p *unixPTY) Close() error {
	p.closeOnce.Do(func() {
		if p.cmd.Process != nil && !p.exited.Load() {
			pid := p.cmd.Process.Pid
			_ = syscall.Kill(-pid, syscall.SIGHUP)
			time.Sleep(closeGracePeriod)
			if !p.exited.Load() {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		p.closeErr = p.file.Close()
	})
	return p.closeErr
}
