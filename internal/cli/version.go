package cli

import (
	"context"
	"flag"
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is set at build time via:
//
//	go build -ldflags "-X github.com/waltzofpearls/deepcove/internal/cli.Version=v0.1.0"
//
// Falls back to the module's VCS info embedded by Go 1.18+.
var Version = "dev"

func cmdVersion(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	var short bool
	fs.BoolVar(&short, "short", false, "print only the version string")
	if err := fs.Parse(args); err != nil {
		return err
	}

	commit := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				commit = s.Value
			}
		}
	}

	if short {
		fmt.Println(Version)
		return nil
	}
	fmt.Printf("version: %s\n", Version)
	if commit != "" {
		fmt.Printf("commit:  %s\n", commit)
	}
	fmt.Printf("go:      %s\n", runtime.Version())
	fmt.Printf("os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}
