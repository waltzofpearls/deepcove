package cloudhv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppArmorProfile(t *testing.T) {
	profile := AppArmorProfile(
		"/root/.config/deepcove/vm-artifacts/passt/7e8d49b/passt",
		"/root/.config/deepcove/vm-artifacts/virtiofsd/v1.14.0/virtiofsd",
	)

	cases := []struct {
		name string
		want string
	}{
		{name: "names the passt profile", want: "profile deepcove-passt "},
		{name: "covers every passt version", want: "/root/.config/deepcove/vm-artifacts/passt/*/passt"},
		{name: "names the virtiofsd profile", want: "profile deepcove-virtiofsd "},
		{name: "covers every virtiofsd version", want: "/root/.config/deepcove/vm-artifacts/virtiofsd/*/virtiofsd"},
		{name: "grants the namespaces passt isolates itself with", want: "userns,"},
		{name: "confines nothing else", want: "flags=(unconfined)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(profile, tc.want) {
				t.Errorf("profile is missing %q:\n%s", tc.want, profile)
			}
		})
	}
}

func TestPasstRunAsArgsFor(t *testing.T) {
	cases := []struct {
		name  string
		facts hostSandboxFacts
		want  []string
	}{
		{
			name:  "a host that does not restrict namespaces",
			facts: hostSandboxFacts{root: true},
		},
		{
			name:  "restricted, but the profile is installed",
			facts: hostSandboxFacts{namespacesRestricted: true, profileInstalled: true, root: true},
		},
		{
			name:  "restricted with no profile, and we are root",
			facts: hostSandboxFacts{namespacesRestricted: true, root: true},
			want:  []string{"--runas", "0:0"},
		},
		{
			// passt cannot change user unless it starts as root, so asking it to is an error.
			name:  "restricted with no profile, and we are not root",
			facts: hostSandboxFacts{namespacesRestricted: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := passtRunAsArgsFor(tc.facts)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("arg %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFileSaysOne(t *testing.T) {
	cases := []struct {
		name    string
		content string
		write   bool
		want    bool
	}{
		{name: "the restriction is on", content: "1\n", write: true, want: true},
		{name: "the restriction is off", content: "0\n", write: true},
		{name: "a kernel that does not have it", content: "", write: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flag")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := fileSaysOne(path); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFileExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	if fileExists(path) {
		t.Error("a file that was never written reported as there")
	}
	if err := os.WriteFile(path, []byte("profile"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Error("a file that is there reported as missing")
	}
}

func TestInstallAppArmorProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deepcove")
	loaded := ""
	loadProfile = func(p string) error {
		loaded = p
		return nil
	}
	t.Cleanup(func() { loadProfile = runApparmorParser })

	binary := "/home/deep/.config/deepcove/vm-artifacts/passt/7e8d49b/passt"
	virtiofsd := "/home/deep/.config/deepcove/vm-artifacts/virtiofsd/v1.14.0/virtiofsd"
	if err := installAppArmorProfile(path, binary, virtiofsd); err != nil {
		t.Fatalf("installAppArmorProfile: %v", err)
	}

	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "/home/deep/.config/deepcove/vm-artifacts/passt/*/passt") {
		t.Errorf("the installed profile does not cover the binary:\n%s", text)
	}
	if loaded != path {
		t.Errorf("loaded %q, want %q", loaded, path)
	}
}

// A profile the kernel refuses is not installed, whatever landed on disk.
func TestInstallAppArmorProfileReportsALoaderFailure(t *testing.T) {
	loadProfile = func(string) error { return errors.New("apparmor_parser said no") }
	t.Cleanup(func() { loadProfile = runApparmorParser })

	err := installAppArmorProfile(filepath.Join(t.TempDir(), "deepcove"), "/x/passt/v1/passt", "/x/virtiofsd/v1/virtiofsd")
	if err == nil {
		t.Error("want the loader's error")
	}
}
