package session

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestSanitizeExt(t *testing.T) {
	cases := map[string]string{
		"Screenshot.PNG":      ".png",      // uppercased ext
		".tar.gz":             ".gz",       // path.Ext takes the last segment
		"noext":               "",          // nothing usable
		"weird.p!n@g":         ".png",      // strips non-alnum
		"x.verylongextension": ".verylong", // capped at 8 chars
	}
	for in, want := range cases {
		if got := sanitizeExt(path.Ext(in)); got != want {
			t.Errorf("sanitizeExt(Ext(%q)) = %q, want %q", in, got, want)
		}
	}
}

func TestService_UploadFile(t *testing.T) {
	ctx := context.Background()

	t.Run("execs into the sandbox and returns the in-sandbox path", func(t *testing.T) {
		svc, fake, _, _ := newTestService(t)
		sess, err := svc.Create(ctx, CreateRequest{Agent: "claude"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		fake.execResponses = append(fake.execResponses, execResponse{exit: 0})

		resp, err := svc.UploadFile(ctx, sess.ID, "Screenshot.PNG", []byte("imagebytes"))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if !strings.HasPrefix(resp.Path, uploadsDir+"/") {
			t.Errorf("path = %q, want under %q", resp.Path, uploadsDir)
		}
		if !strings.HasSuffix(resp.Name, ".png") {
			t.Errorf("name = %q, want .png ext", resp.Name)
		}
		// The write goes through `sh -c 'mkdir -p … && cat > …'`.
		if len(fake.execCalls) == 0 || fake.execCalls[len(fake.execCalls)-1][0] != "sh" {
			t.Errorf("exec calls = %v, want a final sh -c", fake.execCalls)
		}
		joined := strings.Join(fake.execCalls[len(fake.execCalls)-1], " ")
		if !strings.Contains(joined, "cat >") || !strings.Contains(joined, uploadsDir) {
			t.Errorf("exec cmd = %q, want a cat into %s", joined, uploadsDir)
		}
	})

	t.Run("rejects when not running", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		sess, err := svc.Create(ctx, CreateRequest{Agent: "claude"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := svc.Stop(ctx, sess.ID); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if _, err := svc.UploadFile(ctx, sess.ID, "x.png", []byte("d")); err == nil {
			t.Error("want error uploading to a stopped session, got nil")
		}
	})
}

func TestUploadsDirFor(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	t.Setenv("XDG_CACHE_HOME", "")
	cases := []struct {
		name    string
		runtime sandbox.Sandbox
		want    func(string) bool
	}{
		{
			name: "a VM keeps them in the guest", runtime: newFakeSandbox(),
			want: func(got string) bool { return got == uploadsDir },
		},
		{
			name:    "no sandbox keeps them in the user's cache, with no space",
			runtime: &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/home/me/workspaces"},
			want: func(got string) bool {
				return strings.HasPrefix(got, "/home/me/") && strings.HasSuffix(got, "/deepcove/uploads/s1") &&
					!strings.Contains(got, " ")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{dataDir: "/home/me/Library/Application Support/deepcove"}
			if got := svc.uploadsDirFor(tc.runtime, "s1"); !tc.want(got) {
				t.Errorf("got %q", got)
			}
		})
	}
}

func TestService_Destroy_RemovesNoSandboxUploads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	svc := newBareService(t)
	home := t.TempDir()
	svc.shareRoots = sandbox.ShareRoots{Allowed: []string{home}}
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: home}
	svc.RegisterRuntime(RuntimeSandboxless, provisioner)
	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Runtime: RuntimeSandboxless})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	uploads := svc.uploadsDirFor(provisioner, sess.ID)
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := svc.Destroy(context.Background(), sess.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(uploads); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("uploads folder still there: %v", err)
	}
}
