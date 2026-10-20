package agent

import (
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type relayProcess struct {
	cfg      Config
	actionMu sync.Mutex
	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	started  time.Time
	lastExit string
}

func newRelayProcess(c Config) *relayProcess { return &relayProcess{cfg: c} }

func (p *relayProcess) status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	running := p.cmd != nil && p.cmd.Process != nil
	pid := 0
	if running {
		pid = p.cmd.Process.Pid
	}
	return map[string]any{"running": running, "pid": pid, "started_at": p.started, "last_exit": p.lastExit}
}

func (p *relayProcess) start() error {
	p.actionMu.Lock()
	defer p.actionMu.Unlock()
	return p.startLocked()
}

func (p *relayProcess) startLocked() error {
	p.mu.Lock()
	if p.cmd != nil && p.cmd.Process != nil {
		p.mu.Unlock()
		return fmt.Errorf("relay is already running")
	}
	cmd := exec.Command(p.cfg.RelayBinary, "-config", p.cfg.RelayConfig)
	cmd.Dir = p.cfg.RelayWorkingDir
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("could not start relay: %w", err)
	}
	done := make(chan struct{})
	p.cmd = cmd
	p.done = done
	p.started = time.Now().UTC()
	p.lastExit = ""
	p.mu.Unlock()
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		if p.cmd == cmd {
			if err != nil {
				p.lastExit = err.Error()
			} else {
				p.lastExit = "exited successfully"
			}
			p.cmd = nil
		}
		p.mu.Unlock()
		close(done)
	}()
	return nil
}

func (p *relayProcess) stop() error {
	p.actionMu.Lock()
	defer p.actionMu.Unlock()
	return p.stopLocked()
}

func (p *relayProcess) stopLocked() error {
	p.mu.Lock()
	cmd := p.cmd
	done := p.done
	if cmd == nil || cmd.Process == nil {
		p.mu.Unlock()
		return fmt.Errorf("relay is not running")
	}
	if err := stopProcess(cmd); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("could not stop relay: %w", err)
	}
	p.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		// A relay that does not exit after the graceful interrupt is force-stopped.
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Errorf("relay did not stop gracefully and could not be killed: %w", err)
		}
		select {
		case <-done:
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("timed out waiting for relay to exit")
		}
	}
}

func (p *relayProcess) restart() error {
	p.actionMu.Lock()
	defer p.actionMu.Unlock()
	p.mu.Lock()
	running := p.cmd != nil
	p.mu.Unlock()
	if running {
		if err := p.stopLocked(); err != nil {
			return err
		}
	}
	return p.startLocked()
}
