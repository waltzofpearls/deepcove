package applevf

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// The test binary plays `deepcove vm run`: with fakeVMEnv set it serves an echoing guest instead of
// booting a VM, and with fakeVMJammed it also ignores the stop signal, like a VM stuck in Apple VF.
const (
	fakeVMEnv    = "DEEPCOVE_TEST_FAKE_VM"
	fakeVMJammed = "jammed"
	fakeVMDies   = "dies"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeVMEnv); mode != "" {
		os.Exit(fakeVMRun(mode, os.Args[len(os.Args)-1]))
	}
	os.Exit(m.Run())
}

func fakeVMRun(mode, file string) int {
	if mode == fakeVMDies {
		return 1
	}
	if mode == fakeVMJammed {
		signal.Ignore(syscall.SIGTERM)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return 1
	}
	var cfg RunConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 1
	}
	if err := os.WriteFile(pidFile(cfg.VsockSocket), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 1
	}
	console, err := net.Dial("unix", cfg.ConsoleSocket)
	if err != nil {
		return 1
	}
	defer func() { _ = console.Close() }()
	relay, err := vmproc.ServeGuest(cfg.VsockSocket, func(context.Context, uint32) (net.Conn, error) {
		host, guest := net.Pipe()
		go func() { _, _ = io.Copy(guest, guest) }()
		return host, nil
	})
	if err != nil {
		return 1
	}
	defer func() { _ = relay.Close() }()
	select {} // until signalled, or killed
}

func TestStartVMProcess(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		wantBootErr bool
	}{
		{name: "boots, answers and stops", mode: "echo"},
		{name: "a VM that jams is still gone within the limit", mode: fakeVMJammed},
		{name: "a VM that dies at start is a boot error", mode: fakeVMDies, wantBootErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(fakeVMEnv, tc.mode)
			vmProcess, err := startVMProcess(os.Args[0], shortTempDir(t), testSpec())
			if (err != nil) != tc.wantBootErr {
				t.Fatalf("boot error = %v, want error %v", err, tc.wantBootErr)
			}
			if err != nil {
				return
			}
			conn, err := vmProcess.Dial(context.Background(), 1024)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			assertEchoes(t, conn)
			_ = conn.Close()

			start := time.Now()
			_ = vmProcess.Stop()
			if vmProcess.Running() {
				t.Error("the VM's process is still running after Stop")
			}
			// Signal, wait 5s, kill, wait: never more than about 10s.
			if elapsed := time.Since(start); elapsed > 12*time.Second {
				t.Errorf("Stop took %s", elapsed)
			}
		})
	}
}

// shortTempDir keeps socket paths inside the platform limit; t.TempDir() is already too long on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testSpec() vm.Spec {
	return vm.Spec{KernelPath: "/k/vmlinux", BaseImage: "/i/base.ext4", CPUCount: 1, MemoryBytes: 256 << 20}
}

func assertEchoes(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ping" {
		t.Errorf("got %q, %v; want ping back", got, err)
	}
}
