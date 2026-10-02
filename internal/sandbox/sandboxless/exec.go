//go:build !windows

package sandboxless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Exec runs a command on this machine, in the session's environment.
func (d *Driver) Exec(ctx context.Context, instanceID sandbox.ID, spec sandbox.ExecSpec) (sandbox.Process, error) {
	inst, err := d.lookup(instanceID)
	if err != nil {
		return nil, err
	}
	// The machine is up whether or not the session is, so this is the only thing standing between a
	// stopped session and a command running as the user.
	d.mu.Lock()
	running := inst.running
	d.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("the session is not running: %w", sandbox.ErrConflict)
	}
	if len(spec.Cmd) == 0 {
		return nil, fmt.Errorf("sandboxless: a command is required")
	}

	argv := withTmuxSocket(spec.Cmd, inst.socket)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.WorkingDir
	cmd.Env = d.environment(inst, spec.Env)

	if spec.Tty {
		// No Setpgid here: pty.Start asks for a new session, which already makes this a group leader,
		// and asking for both fails the exec with EPERM.
		return startWithTTY(cmd)
	}
	// Its own process group, so Close takes down whatever it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return startWithPipes(cmd, spec.Stdin)
}

func (d *Driver) environment(inst *instance, callEnv map[string]string) []string {
	env := allowedEnvVars()
	for name, value := range inst.spec.Env {
		env[name] = value
	}
	for name, value := range callEnv {
		env[name] = value
	}
	// Anything in here that runs tmux gets this instance's own folder, never the user's server. A
	// missing folder would send tmux back to the default socket, so this one has to exist, and it does.
	env["TMUX_TMPDIR"] = inst.dir
	delete(env, "TMUX")
	delete(env, "TMUX_PANE")
	// HOME is the agent's, so the rc files that define what this calls never load and every prompt
	// prints an error.
	delete(env, "PROMPT_COMMAND")
	return sortedEnv(env)
}

// What the agent gets from the machine. Everything else deepcove holds is deepcove's: a Tailscale auth
// key, the settings in $DATA_DIR/env, whatever the service file sets.
var agentEnvAllowlist = []string{
	"HOME", "USER", "LOGNAME", "SHELL", // who the agent is, and where its own files live
	"PATH", "TMPDIR", // where it finds its tools and its scratch space
	"TERM", "COLORTERM", "LANG", "TZ", // what its terminal can draw, and in which locale
	"SSH_AUTH_SOCK",                         // git over ssh, through the user's own agent
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", // the machine's proxy
	// A proxy that inspects TLS needs its CA trusted, and each stack reads its own name: openssl and
	// Go, then Node, python requests, curl.
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
	// Where tools keep config, cache, data and state, for a user who moved them.
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
}

func allowedEnvVars() map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, ok := splitEnv(entry)
		if ok && (slices.Contains(agentEnvAllowlist, name) || strings.HasPrefix(name, "LC_")) {
			env[name] = value
		}
	}
	return env
}

func sortedEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for name, value := range env {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}

// Without this, tmux uses the socket the user's own session is on, and stopping a sandboxless
// instance kills the terminal they are sitting in.
func withTmuxSocket(argv []string, socket string) []string {
	if filepath.Base(argv[0]) != "tmux" {
		return argv
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], "-S", socket)
	return append(out, argv[1:]...)
}

func splitEnv(entry string) (name, value string, ok bool) {
	for i := range len(entry) {
		if entry[i] == '=' {
			return entry[:i], entry[i+1:], i > 0
		}
	}
	return "", "", false
}

func startWithTTY(cmd *exec.Cmd) (sandbox.Process, error) {
	file, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("sandboxless: start %s: %w", cmd.Path, err)
	}
	return &process{cmd: cmd, tty: file, stdout: file, stdin: file}, nil
}

func startWithPipes(cmd *exec.Cmd, stdin io.Reader) (sandbox.Process, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	var input io.WriteCloser
	if stdin != nil {
		cmd.Stdin = stdin
	} else {
		if input, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandboxless: start %s: %w", cmd.Path, err)
	}
	return &process{cmd: cmd, stdout: stdout, stderr: stderr, stdin: input}, nil
}

// A TTY is one stream, so Stdout carries everything and Stderr stays empty.
type process struct {
	cmd    *exec.Cmd
	tty    *os.File
	stdout io.ReadCloser
	stderr io.ReadCloser
	stdin  io.WriteCloser

	waitOnce sync.Once
	waitErr  error
	exitCode int
}

func (p *process) Stdout() io.ReadCloser {
	return p.stdout
}

func (p *process) Stderr() io.ReadCloser {
	if p.stderr == nil {
		return io.NopCloser(nopReader{})
	}
	return p.stderr
}

func (p *process) Stdin() io.WriteCloser {
	if p.stdin == nil {
		return nopWriteCloser{}
	}
	return p.stdin
}

func (p *process) Wait(ctx context.Context) (int, error) {
	done := make(chan struct{})
	go func() {
		p.waitOnce.Do(p.wait)
		close(done)
	}()
	select {
	case <-done:
		return p.exitCode, p.waitErr
	case <-ctx.Done():
		closeErr := p.Close()
		<-done
		return p.exitCode, errors.Join(ctx.Err(), closeErr)
	}
}

func (p *process) wait() {
	err := p.cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		p.exitCode = 0
	case errors.As(err, &exit):
		p.exitCode = exit.ExitCode()
	default:
		p.waitErr = err
	}
	p.waitErr = errors.Join(p.waitErr, p.closeTTY())
}

func (p *process) Close() error {
	var killErr error
	if p.cmd.Process != nil {
		// The group, not the process: tmux attach and a shell both leave children behind.
		// ESRCH says it is gone already, which is what Close wanted.
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			killErr = err
		}
	}
	return errors.Join(killErr, p.closeTTY())
}

// Wait and Close both end up here, so the second one finds the file already closed.
func (p *process) closeTTY() error {
	if p.tty == nil {
		return nil
	}
	if err := p.tty.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

func (p *process) Signal(s os.Signal) error {
	if p.cmd.Process == nil {
		return fmt.Errorf("sandboxless: the process is not running")
	}
	sig, ok := s.(syscall.Signal)
	if !ok {
		return fmt.Errorf("sandboxless: %s is not a unix signal", s)
	}
	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

func (p *process) Resize(cols, rows uint16) error {
	if p.tty == nil {
		return sandbox.ErrNotSupported
	}
	return pty.Setsize(p.tty, &pty.Winsize{Cols: cols, Rows: rows})
}

type nopReader struct{}

func (nopReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (nopWriteCloser) Close() error {
	return nil
}
