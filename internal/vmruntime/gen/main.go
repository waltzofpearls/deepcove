// Command gen writes versions.json from the versions in the vmruntime package, so the release build reads
// the same versions the code does without needing Go or a deepcove binary.
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

const path = "versions.json"

func main() {
	body, err := json.MarshalIndent(vmruntime.Versions(), "", "  ")
	if err != nil {
		log.Fatalf("gen: %v", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		log.Fatalf("gen: %v", err)
	}
}
