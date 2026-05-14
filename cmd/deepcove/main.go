// deepcove — a self-hosted, isolated coding agent studio on your tailnet.
//
// Thin entry point. All command logic lives in internal/cli.
package main

import (
	"os"

	"github.com/waltzofpearls/deepcove/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
