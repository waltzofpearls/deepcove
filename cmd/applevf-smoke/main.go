// Command applevf-smoke is a development harness for booting one Apple Virtualization.framework VM and
// bridging its console to the terminal, so the runtime's boot path can be validated on real hardware
// before the full driver exists. It must be codesigned with the com.apple.security.virtualization
// entitlement to run (see build/virtualization.entitlements and `make applevf-smoke`).
//
// Two boot modes:
//   - initrd only (PUI PUI Linux): --kernel vmlinux --initrd initramfs.cpio.gz --cmdline "console=hvc0"
//     Proves VF + codesigning + console I/O with no dependency on the deepcove image pipeline.
//   - deepcove base image: --kernel vmlinuz --base rootfs.ext4  (default cmdline boots our guest init)
//     Proves the image pipeline + guest init + overlay + tmux launch.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/applevf"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "applevf-smoke:", err)
		os.Exit(1)
	}
}

func run() error {
	kernel := flag.String("kernel", "", "path to the uncompressed Linux kernel (required)")
	initrd := flag.String("initrd", "", "path to an initrd/initramfs (initrd-only boot)")
	base := flag.String("base", "", "path to a read-only ext4 base image (deepcove boot)")
	scratch := flag.String("scratch", "", "path to a writable scratch disk (optional)")
	image := flag.String("image", "", "OCI reference to build a base image from, injecting the guest init")
	guestInit := flag.String("guest-init", "internal/guestasset/deepcove-guest-init", "guest init binary to inject when --image is set")
	launch := flag.String("launch", "sleep infinity", "shell command the guest runs as its session entrypoint")
	out := flag.String("out", "dist/guest/smoke-rootfs.ext4", "where to write the ext4 base built from --image")
	cmdline := flag.String("cmdline", "", "kernel command line (default boots the deepcove guest init)")
	cpu := flag.Uint("cpu", 2, "vCPU count")
	memMiB := flag.Uint64("mem", 1024, "memory in MiB")
	diskMiB := flag.Uint64("disk", 0, "cap the guest's writable layer, in MiB (0 leaves it at the tmpfs default)")
	execCmd := flag.String("exec", "", "after boot, run this command in the guest over vsock and print the result")
	execTTY := flag.Bool("exec-tty", false, "run the --exec command on a pseudo terminal")
	execStdin := flag.String("exec-stdin", "", "feed this file to the --exec command's stdin, closing it at EOF")
	credsFile := flag.String("creds", "", "JSON file backing the credential broker (enables it)")
	agent := flag.String("agent", "", "agent name whose credentials the guest brokers with the host")
	flag.Parse()

	if *kernel == "" {
		return fmt.Errorf("--kernel is required")
	}

	// --image builds the ext4 base (pull, flatten, inject the guest init) then boots it.
	if *image != "" {
		fmt.Fprintf(os.Stderr, "applevf-smoke: building base image from %s -> %s\n", *image, *out)
		if err := vm.BuildSmokeImage(context.Background(), *image, *out, *guestInit); err != nil {
			return fmt.Errorf("build base image: %w", err)
		}
		*base = *out
	}

	handle, err := applevf.BootSmoke(applevf.SmokeSpec{
		KernelPath:   *kernel,
		InitrdPath:   *initrd,
		BaseImage:    *base,
		ScratchImage: *scratch,
		Cmdline:      *cmdline,
		CPUCount:     *cpu,
		MemoryBytes:  *memMiB << 20,
		OverlayBytes: int64(*diskMiB) << 20,
		CredStore:    credStore(*credsFile),
		Entrypoint:   []string{"/bin/sh", "-c", *launch},
		Agent:        *agent,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "applevf-smoke: VM started; console follows. Ctrl-C to stop.")

	// Bridge the guest console to this terminal: guest output to our stdout, our stdin to the guest.
	console := handle.Console()
	go func() { _, _ = io.Copy(os.Stdout, console) }()
	go func() { _, _ = io.Copy(console, os.Stdin) }()

	if *execCmd != "" {
		if err := runExec(handle, *execCmd, *execTTY, *execStdin); err != nil {
			fmt.Fprintln(os.Stderr, "applevf-smoke: exec failed:", err)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Fprintln(os.Stderr, "\napplevf-smoke: stopping VM")
	return handle.Stop()
}

// feedStdin streams a file into the running command and closes stdin, so a command reading to EOF can
// finish. Runs inline rather than in a goroutine: the harness has nothing else to do until it is sent.
func feedStdin(proc sandbox.Process, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open --exec-stdin: %w", err)
	}
	defer func() { _ = f.Close() }()

	w := proc.Stdin()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("write stdin to the guest: %w", err)
	}
	return w.Close()
}

// runExec proves the vsock exec path end to end: dial the guest's exec service, run a command, stream its
// output, and report the exit status.
func runExec(handle applevf.SmokeMachine, command string, tty bool, stdinPath string) error {
	fmt.Fprintf(os.Stderr, "\napplevf-smoke: exec %q (tty=%v)\n", command, tty)
	proc, err := handle.Exec(context.Background(), guestexec.ExecRequest{
		Argv: []string{"/bin/sh", "-c", command},
		Tty:  tty, Cols: 100, Rows: 30,
	})
	if err != nil {
		return err
	}
	defer func() { _ = proc.Close() }()

	// Feeding stdin and closing it is what a file upload does, so the harness needs to be able to do it
	// too. Not being able to is why a runtime that silently dropped stdin looked healthy here.
	if stdinPath != "" {
		if err := feedStdin(proc, stdinPath); err != nil {
			return err
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyPrefixed("exec-out", proc.Stdout()) }()
	go func() { defer wg.Done(); copyPrefixed("exec-err", proc.Stderr()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	code, err := proc.Wait(ctx)
	wg.Wait()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "applevf-smoke: exec exited with code %d\n", code)
	return nil
}

func copyPrefixed(tag string, r io.ReadCloser) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", tag, sc.Text())
	}
}

// credStore enables the credential broker when a backing file is given.
func credStore(path string) credbroker.Store {
	if path == "" {
		return nil
	}
	return newFileStore(path)
}
