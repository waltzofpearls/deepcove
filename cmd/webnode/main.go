// Command webnode builds the hosted web node into web-node/dist: the UI bundle from ui/build, set up the
// way `deepcove dashboard` serves it, plus the response headers in web-node/_headers with the hashes of
// the page's inline scripts filled in. The WASM file is left out, since it is too big for a static asset
// and goes to R2 instead; its path inside the bundle is printed for the deploy step. It runs from the
// repository root.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/files"
	"github.com/waltzofpearls/deepcove/internal/ui"
)

// wasmDir is where the page asks for the WASM file, and the only path web-node/wrangler.jsonc sends to R2.
// A WASM file anywhere else would upload fine and then never be served.
const wasmDir = "_app/immutable/assets/"

// inlineScriptHashesMark is where web-node/_headers wants the hashes of index.html's inline scripts.
const inlineScriptHashesMark = "INLINE_SCRIPT_HASHES"

// script matches a script element: its attributes, then its text.
var script = regexp.MustCompile(`(?s)<script\b([^>]*)>(.*?)</script>`)

func main() {
	wasm, err := build(
		filepath.Join("ui", "build"),
		filepath.Join("web-node", "_headers"),
		filepath.Join("web-node", "dist"),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "webnode:", err)
		os.Exit(1)
	}
	fmt.Println(wasm)
}

// build copies bundle and the headers file into dist and returns the WASM file's path inside the bundle,
// with forward slashes, which is also the path the page asks for it at.
func build(bundle, headers, dist string) (string, error) {
	if err := os.RemoveAll(dist); err != nil {
		return "", err
	}
	var wasm string
	var indexHTML []byte
	err := filepath.WalkDir(bundle, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(bundle, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dist, rel)
		switch {
		case entry.IsDir():
			return os.MkdirAll(to, 0o755)
		case strings.HasSuffix(path, ".wasm"):
			found := filepath.ToSlash(rel)
			if !strings.HasPrefix(found, wasmDir) {
				return fmt.Errorf("%s is outside %s, the only place web-node/wrangler.jsonc serves WASM from", found, wasmDir)
			}
			if wasm != "" {
				return fmt.Errorf("two WASM files in %s: %s and %s", bundle, wasm, found)
			}
			wasm = found
			return nil
		case rel == "index.html":
			indexHTML, err = writeDashboardIndexHTML(path, to)
			return err
		default:
			return files.Copy(path, to)
		}
	})
	if err != nil {
		return "", err
	}
	if wasm == "" {
		return "", errors.New("no WASM file in " + bundle + ", so the UI was built without it: run make ui-build")
	}
	if err := writeHeaders(headers, filepath.Join(dist, "_headers"), indexHTML); err != nil {
		return "", err
	}
	return wasm, nil
}

// writeDashboardIndexHTML writes index.html set up like the dashboard, and returns what it wrote.
func writeDashboardIndexHTML(from, to string) ([]byte, error) {
	html, err := os.ReadFile(from)
	if err != nil {
		return nil, err
	}
	shell := ui.DashboardIndexHTML(html)
	// Served unchanged, the page would look for an API on app.deepcove.dev that is not there.
	if bytes.Equal(shell, html) {
		return nil, fmt.Errorf("%s has no </head> to add the dashboard settings before", from)
	}
	return shell, os.WriteFile(to, shell, 0o644)
}

// writeHeaders copies the headers file, with the hashes of indexHTML's inline scripts in its policy. The
// browser runs only the inline scripts whose hash is listed, so a missing one breaks the page.
func writeHeaders(from, to string, indexHTML []byte) error {
	headers, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if !bytes.Contains(headers, []byte(inlineScriptHashesMark)) {
		return fmt.Errorf("%s has no %s to put the inline script hashes in", from, inlineScriptHashesMark)
	}
	hashes := strings.Join(inlineScriptHashes(indexHTML), " ")
	return os.WriteFile(to, bytes.ReplaceAll(headers, []byte(inlineScriptHashesMark), []byte(hashes)), 0o644)
}

// inlineScriptHashes returns a policy source for each inline script in html, the form a browser checks
// the script's text against.
func inlineScriptHashes(html []byte) []string {
	var hashes []string
	for _, match := range script.FindAllSubmatch(html, -1) {
		attributes, text := match[1], match[2]
		if bytes.Contains(attributes, []byte("src=")) {
			continue
		}
		sum := sha256.Sum256(text)
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}
