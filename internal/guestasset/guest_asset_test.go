package guestasset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstall(t *testing.T) {
	t.Run("writes the init and makes it executable", func(t *testing.T) {
		dir := t.TempDir()
		path, err := install(dir, []byte("init-v1"))
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		if got := filepath.Base(path); got != Name {
			t.Errorf("name: got %q, want %q", got, Name)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(content) != "init-v1" {
			t.Errorf("content: got %q, want %q", content, "init-v1")
		}
		// The kernel runs this directly, so the bit matters.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("mode %v is not executable", info.Mode().Perm())
		}
	})

	t.Run("creates the directory when it does not exist", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "vm-guest")
		if _, err := install(dir, []byte("init-v1")); err != nil {
			t.Fatalf("install: %v", err)
		}
	})

	// A restart that changed nothing should leave the file alone, not rewrite it.
	t.Run("leaves an identical file untouched", func(t *testing.T) {
		dir := t.TempDir()
		path, err := install(dir, []byte("init-v1"))
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}

		if _, err := install(dir, []byte("init-v1")); err != nil {
			t.Fatalf("second install: %v", err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if !before.ModTime().Equal(after.ModTime()) {
			t.Error("an unchanged init was rewritten")
		}
	})

	// A new deepcove must replace the init from the previous one, or sessions boot stale code.
	t.Run("replaces an init whose content changed", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := install(dir, []byte("init-v1")); err != nil {
			t.Fatalf("install: %v", err)
		}
		path, err := install(dir, []byte("init-v2"))
		if err != nil {
			t.Fatalf("second install: %v", err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(content) != "init-v2" {
			t.Errorf("content: got %q, want the new one", content)
		}
	})

	t.Run("a tree with no init built reports that rather than writing an empty file", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := install(dir, nil); err == nil {
			t.Fatal("expected an error for an empty init")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("left %d files behind, want none", len(entries))
		}
	})

	// Nothing partial should survive a failed write, so no leftover temp files.
	t.Run("leaves no temporary files behind", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := install(dir, []byte("init-v1")); err != nil {
			t.Fatalf("install: %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		if len(entries) != 1 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("directory holds %v, want only %s", names, Name)
		}
	})
}
