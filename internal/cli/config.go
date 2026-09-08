package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/waltzofpearls/deepcove/internal/config"
)

// cmdConfig handles `deepcove config`. These settings live in a file in the data dir rather than in a
// service definition, so a background service reads them too and a token is not left world readable.
//
// Usage:
//
//	deepcove config list
//	deepcove config set KEY=VALUE ...
//	deepcove config unset KEY ...
func cmdConfig(ctx context.Context, args []string) error {
	if len(args) == 0 {
		configUsage(os.Stderr)
		return fmt.Errorf("config needs a verb (e.g. `deepcove config list`)")
	}
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	switch args[0] {
	case "-h", "--help", "help":
		configUsage(os.Stdout)
		return nil
	case "list":
		return listConfig(cfg.DataDir)
	case "set":
		if len(args) == 1 {
			return fmt.Errorf("usage: deepcove config set KEY=VALUE")
		}
		if err := config.SetEnvFileValues(cfg.DataDir, args[1:]); err != nil {
			return err
		}
		fmt.Printf("stored in %s\n", config.EnvFilePath(cfg.DataDir))
		return listConfig(cfg.DataDir)
	case "unset":
		if len(args) == 1 {
			return fmt.Errorf("usage: deepcove config unset KEY")
		}
		if err := config.UnsetEnvFileValues(cfg.DataDir, args[1:]); err != nil {
			return err
		}
		return listConfig(cfg.DataDir)
	}
	configUsage(os.Stderr)
	return fmt.Errorf("unknown config verb %q", args[0])
}

// listConfig prints what is stored, values and all. The file is yours alone, and a masked token cannot
// be checked against the one you meant to store.
func listConfig(dataDir string) error {
	values, err := config.EnvFileValues(dataDir)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		fmt.Printf("nothing stored in %s\n", config.EnvFilePath(dataDir))
		return nil
	}
	for _, value := range values {
		fmt.Println(value)
	}
	return nil
}

func configUsage(w *os.File) {
	lines := []string{
		"deepcove config: settings this node keeps, which a background service reads too",
		"",
		"Usage:",
		"  deepcove config list",
		"  deepcove config set KEY=VALUE [KEY=VALUE ...]",
		"  deepcove config unset KEY [KEY ...]",
		"",
		"Anything you export yourself still wins over what is stored here.",
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}
