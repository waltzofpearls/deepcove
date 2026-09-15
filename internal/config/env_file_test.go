package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvLine(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		key   string
		value string
		ok    bool
	}{
		{name: "a setting", line: "GITHUB_TOKEN=abc123", key: "GITHUB_TOKEN", value: "abc123", ok: true},
		{name: "spaces around it", line: "  LOG_LEVEL = debug  ", key: "LOG_LEVEL", value: "debug", ok: true},
		{name: "an empty value clears it", line: "LOG_LEVEL=", key: "LOG_LEVEL", value: "", ok: true},
		{name: "a value holding an equals", line: "A=b=c", key: "A", value: "b=c", ok: true},
		{name: "a comment", line: "# GITHUB_TOKEN=abc"},
		{name: "a blank line", line: "   "},
		{name: "no name", line: "=abc"},
		{name: "no equals", line: "GITHUB_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, value, ok := parseEnvLine(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (key != tc.key || value != tc.value) {
				t.Errorf("got %q=%q, want %q=%q", key, value, tc.key, tc.value)
			}
		})
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "# stored settings\nGITHUB_TOKEN=from-the-file\nLOG_LEVEL=debug\n")

	t.Setenv("LOG_LEVEL", "info")
	// t.Setenv registers the restore, then the test unsets it so the file can supply it.
	t.Setenv("GITHUB_TOKEN", "")
	if err := os.Unsetenv("GITHUB_TOKEN"); err != nil {
		t.Fatal(err)
	}

	if err := loadEnvFile(dir); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv("GITHUB_TOKEN"); got != "from-the-file" {
		t.Errorf("GITHUB_TOKEN = %q, want the stored value", got)
	}
	// What you exported for this run beats what is stored.
	if got := os.Getenv("LOG_LEVEL"); got != "info" {
		t.Errorf("LOG_LEVEL = %q, want the exported value", got)
	}
}

func TestLoadEnvFileWithoutAFile(t *testing.T) {
	if err := loadEnvFile(t.TempDir()); err != nil {
		t.Errorf("a missing file is normal, got %v", err)
	}
}

func TestSetEnvFileValues(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "# keep me\nGITHUB_TOKEN=old\nLOG_LEVEL=info\n")

	if err := SetEnvFileValues(dir, []string{"GITHUB_TOKEN=new", "DEEPCOVE_DEFAULT_RUNTIME=cloudhv"}); err != nil {
		t.Fatalf("SetEnvFileValues: %v", err)
	}

	want := "# keep me\nGITHUB_TOKEN=new\nLOG_LEVEL=info\nDEEPCOVE_DEFAULT_RUNTIME=cloudhv\n"
	if got := read(t, dir); got != want {
		t.Errorf("file is\n%q\nwant\n%q", got, want)
	}
	info, err := os.Stat(EnvFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode %o, want 600: a token lives here", mode)
	}
}

func TestSetEnvFileValuesCreatesTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-there-yet")
	if err := SetEnvFileValues(dir, []string{"GITHUB_TOKEN=abc"}); err != nil {
		t.Fatalf("SetEnvFileValues: %v", err)
	}
	if got, want := read(t, dir), "GITHUB_TOKEN=abc\n"; got != want {
		t.Errorf("file is %q, want %q", got, want)
	}
}

func TestSetEnvFileValuesRefusesJunk(t *testing.T) {
	if err := SetEnvFileValues(t.TempDir(), []string{"not-a-setting"}); err == nil {
		t.Error("want an error for an entry that is not KEY=VALUE")
	}
}

func write(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(EnvFilePath(dir), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir string) string {
	t.Helper()
	text, err := os.ReadFile(EnvFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestUnsetEnvFileValues(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "# keep me\nGITHUB_TOKEN=abc\nLOG_LEVEL=debug\n")

	if err := UnsetEnvFileValues(dir, []string{"GITHUB_TOKEN", "NEVER_SET"}); err != nil {
		t.Fatalf("UnsetEnvFileValues: %v", err)
	}

	if got, want := read(t, dir), "# keep me\nLOG_LEVEL=debug\n"; got != want {
		t.Errorf("file is %q, want %q", got, want)
	}
}

// Nothing left to keep means no file, rather than an empty one nobody can explain.
func TestUnsetEnvFileValuesRemovesAnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "GITHUB_TOKEN=abc\n")

	if err := UnsetEnvFileValues(dir, []string{"GITHUB_TOKEN"}); err != nil {
		t.Fatalf("UnsetEnvFileValues: %v", err)
	}

	if _, err := os.Stat(EnvFilePath(dir)); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
}

func TestUnsetEnvFileValuesWithoutAFile(t *testing.T) {
	if err := UnsetEnvFileValues(t.TempDir(), []string{"GITHUB_TOKEN"}); err != nil {
		t.Errorf("unsetting what was never set is not an error, got %v", err)
	}
}

func TestEnvFileValues(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "# a comment\n\nGITHUB_TOKEN=abc\n  LOG_LEVEL = debug \nnot a setting\n")

	values, err := EnvFileValues(dir)
	if err != nil {
		t.Fatalf("EnvFileValues: %v", err)
	}

	want := []string{"GITHUB_TOKEN=abc", "LOG_LEVEL=debug"}
	if len(values) != len(want) {
		t.Fatalf("got %v, want %v", values, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("value %d = %q, want %q", i, values[i], want[i])
		}
	}
}

func TestEnvFileValuesWithoutAFile(t *testing.T) {
	values, err := EnvFileValues(t.TempDir())
	if err != nil || values != nil {
		t.Errorf("got %v, %v, want nothing and no error", values, err)
	}
}

// The file usually exists before a token is stored in it, and WriteFile leaves an existing file's mode
// alone, so a 0644 file would keep a token in plain view.
func TestSetEnvFileValuesTightensAnOpenFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "LOG_LEVEL=info\n")
	if err := os.Chmod(EnvFilePath(dir), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetEnvFileValues(dir, []string{"GITHUB_TOKEN=abc"}); err != nil {
		t.Fatalf("SetEnvFileValues: %v", err)
	}

	info, err := os.Stat(EnvFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode %o, want 600", mode)
	}
}

func TestEnvFileOpenToOthers(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
		want bool
	}{
		{name: "only the owner", mode: 0o600},
		{name: "the group can read", mode: 0o640, want: true},
		{name: "anyone can read", mode: 0o644, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "GITHUB_TOKEN=abc\n")
			if err := os.Chmod(EnvFilePath(dir), tc.mode); err != nil {
				t.Fatal(err)
			}
			open, err := EnvFileOpenToOthers(dir)
			if err != nil {
				t.Fatalf("EnvFileOpenToOthers: %v", err)
			}
			if open != tc.want {
				t.Errorf("open = %v, want %v", open, tc.want)
			}
		})
	}
}

func TestEnvFileOpenToOthersWithoutAFile(t *testing.T) {
	open, err := EnvFileOpenToOthers(t.TempDir())
	if err != nil || open {
		t.Errorf("got %v, %v, want false and no error", open, err)
	}
}
