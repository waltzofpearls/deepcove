//go:build darwin

package applevf

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// Run holds one VM in this process until ctx ends or the VM stops. A VM that jams can then only take this
// process with it, never deepcove.
func Run(ctx context.Context, cfg RunConfig, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Written first, so even a VM that jams while booting is found and killed by the next deepcove.
	pidLock, err := holdPIDFile(pidFile(cfg.VsockSocket))
	if err != nil {
		return err
	}
	defer func() { _ = pidLock.Close() }()
	go stopWhenOrphaned(cancel)
	booted, err := bootVM(cfg.Spec, logger)
	if err != nil {
		return err
	}
	console, err := net.Dial("unix", cfg.ConsoleSocket)
	if err != nil {
		_ = booted.Stop()
		return fmt.Errorf("applevf: reach deepcove for the console: %w", err)
	}
	go func() { _, _ = io.Copy(console, booted.console) }()
	go func() { _, _ = io.Copy(booted.console, console) }()

	relay, err := vmproc.ServeGuest(cfg.VsockSocket, booted.Dial)
	if err != nil {
		_ = booted.Stop()
		return err
	}
	defer func() { _ = relay.Close() }()
	for _, port := range cfg.Spec.HostPorts {
		listener, err := booted.Listen(port)
		if err != nil {
			_ = booted.Stop()
			return fmt.Errorf("applevf: listen for the guest on port %d: %w", port, err)
		}
		go vmproc.ForwardFromGuest(listener, cfg.VsockSocket, port)
	}

	select {
	case <-ctx.Done():
		// Asked to stop. A jammed VM hangs here, and deepcove kills this process after a few seconds.
		return booted.Stop()
	case <-booted.stoppedOnItsOwn():
		return nil
	}
}

// holdPIDFile writes this process's PID and locks the file until it is closed. The lock is how the next
// deepcove tells this process from a later one that got the same PID.
func holdPIDFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// stopWhenOrphaned ends the VM once deepcove is gone. macOS hands an orphan to launchd, PID 1.
func stopWhenOrphaned(stop context.CancelFunc) {
	for os.Getppid() != 1 {
		time.Sleep(time.Second)
	}
	stop()
}
