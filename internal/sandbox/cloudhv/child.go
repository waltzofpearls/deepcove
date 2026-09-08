package cloudhv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const killTimeout = 5 * time.Second

// child is a supervised host process: the VMM, or one of the helpers it needs.
type child struct {
	name    string
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
}

// startChild runs binary and reaps it in the background.
func startChild(name, binary string, argv []string) (*child, error) {
	cmd := exec.Command(binary, argv...)
	// Its own process group, so stopping it takes anything it started with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The process's own diagnostics, which are not the guest console.
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cloudhv: start %s: %w", commandLine(binary, argv), err)
	}
	c := &child{name: name, cmd: cmd, exited: make(chan struct{})}
	go func() {
		c.waitErr = cmd.Wait()
		close(c.exited)
	}()
	return c, nil
}

func (c *child) Running() bool {
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

// WaitStopped waits for the process to exit.
func (c *child) WaitStopped(timeout time.Duration) error {
	select {
	case <-c.exited:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("cloudhv: %s did not exit within %s", c.name, timeout)
	}
}

// Stop signals the process group, then kills it if it will not go.
func (c *child) Stop() error {
	if !c.Running() {
		return nil
	}
	if err := c.signalGroup(syscall.SIGTERM); err != nil {
		return err
	}
	if err := c.WaitStopped(killTimeout); err == nil {
		return nil
	}
	if err := c.signalGroup(syscall.SIGKILL); err != nil {
		return err
	}
	return c.WaitStopped(killTimeout)
}

// signalGroup signals the process and anything it started. It leads its own group, so a helper it
// spawned cannot outlive it.
func (c *child) signalGroup(sig syscall.Signal) error {
	err := syscall.Kill(-c.cmd.Process.Pid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// waitForFile blocks until path exists, the process exits, or the wait runs out.
func waitForFile(c *child, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-c.exited:
			return fmt.Errorf("cloudhv: %s exited before it was ready: %w", c.name, c.waitErr)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cloudhv: %s did not create %s within %s", c.name, path, timeout)
		}
		time.Sleep(readyPoll)
	}
}
