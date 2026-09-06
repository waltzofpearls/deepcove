package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

// Version is set at build time via:
//
//	go build -ldflags "-X github.com/waltzofpearls/deepcove/internal/cli.Version=v0.1.0"
//
// Falls back to the module's VCS info embedded by Go 1.18+.
var Version = "dev"

type versionReport struct {
	Version string              `json:"version"`
	Commit  string              `json:"commit,omitempty"`
	Go      string              `json:"go"`
	OS      string              `json:"os"`
	Arch    string              `json:"arch"`
	VM      vmruntime.Artifacts `json:"vm"`
}

func newVersionReport() versionReport {
	commit := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				commit = s.Value
			}
		}
	}
	return versionReport{
		Version: Version,
		Commit:  commit,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		VM:      vmruntime.Versions(),
	}
}

func (r versionReport) text() string {
	lines := []string{fmt.Sprintf("version: %s", r.Version)}
	if r.Commit != "" {
		lines = append(lines, fmt.Sprintf("commit:  %s", r.Commit))
	}
	lines = append(lines,
		fmt.Sprintf("go:      %s", r.Go),
		fmt.Sprintf("os/arch: %s/%s", r.OS, r.Arch),
		"vm artifacts:",
		fmt.Sprintf("  guest-kernel     %s (kata %s)", r.VM.GuestKernel.Version, r.VM.GuestKernel.KataRelease),
		fmt.Sprintf("  cloud-hypervisor %s", r.VM.CloudHypervisor.Version),
		fmt.Sprintf("  virtiofsd        %s", r.VM.Virtiofsd.Version),
		fmt.Sprintf("  passt            %s", r.VM.Passt.Version),
	)
	return strings.Join(lines, "\n") + "\n"
}

func (r versionReport) writeJSON(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r)
}

func cmdVersion(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	var short, asJSON bool
	fs.BoolVar(&short, "short", false, "print only the version string")
	fs.BoolVar(&asJSON, "json", false, "print everything as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if short && asJSON {
		return errors.New("version: choose either -short or -json, not both")
	}

	report := newVersionReport()
	switch {
	case short:
		fmt.Println(report.Version)
	case asJSON:
		return report.writeJSON(os.Stdout)
	default:
		fmt.Print(report.text())
	}
	return nil
}
