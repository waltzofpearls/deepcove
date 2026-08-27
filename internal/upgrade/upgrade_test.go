package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
	}{
		{"darwin", "arm64", "deepcove_v1.2.3_darwin-arm64.zip"},
		{"darwin", "amd64", "deepcove_v1.2.3_darwin-amd64.zip"},
		{"windows", "amd64", "deepcove_v1.2.3_windows-amd64.zip"},
		{"linux", "amd64", "deepcove_v1.2.3_linux-amd64.tar.gz"},
		{"linux", "arm64", "deepcove_v1.2.3_linux-arm64.tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.goos+"-"+tc.goarch, func(t *testing.T) {
			if got := AssetName("v1.2.3", tc.goos, tc.goarch); got != tc.want {
				t.Errorf("AssetName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBinaryName(t *testing.T) {
	if got := BinaryName("windows"); got != "deepcove.exe" {
		t.Errorf("windows = %q", got)
	}
	if got := BinaryName("linux"); got != "deepcove" {
		t.Errorf("linux = %q", got)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := strings.Join([]string{
		"AAAA1111  deepcove_v1_linux-amd64.tar.gz",
		"bbbb2222 *deepcove_v1_darwin-arm64.zip",
		"garbage",
		"",
	}, "\n")
	cases := []struct {
		name    string
		asset   string
		want    string
		wantErr bool
	}{
		{"lowercased", "deepcove_v1_linux-amd64.tar.gz", "aaaa1111", false},
		{"binary mode star", "deepcove_v1_darwin-arm64.zip", "bbbb2222", false},
		{"not listed", "deepcove_v1_windows-amd64.zip", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checksumFor([]byte(sums), tc.asset)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("wanted an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("checksumFor: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// mockReleaseServer serves one release whose assets are the given files, so the client under test walks
// the same path it does against GitHub.
func mockReleaseServer(t *testing.T, tag string, assets map[string][]byte, wantToken string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantToken {
			t.Errorf("Authorization = %q, want %q", got, wantToken)
		}
		suffix := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/releases/")
		if suffix != "latest" && suffix != "tags/"+tag {
			http.NotFound(w, r)
			return
		}
		release := Release{Tag: tag}
		for name := range assets {
			release.Assets = append(release.Assets, releaseAsset{Name: name, URL: server.URL + "/assets/" + name})
		}
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := assets[strings.TrimPrefix(r.URL.Path, "/assets/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// GitHub sends this, and without it net/http switches to chunked for larger bodies, which
		// would leave the download with no total to show.
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// checksumsFile is the SHA256SUMS the release would carry for these files.
func checksumsFile(files map[string][]byte) []byte {
	var builder strings.Builder
	for name, body := range files {
		if name == checksumsAsset {
			continue
		}
		digest := sha256.Sum256(body)
		fmt.Fprintf(&builder, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	}
	return []byte(builder.String())
}

func TestClientDownload(t *testing.T) {
	const assetName = "deepcove_v1.2.3_linux-amd64.tar.gz"
	body := []byte("an archive")

	cases := []struct {
		name    string
		corrupt bool
		ask     string
		wantErr string
	}{
		{name: "verified", ask: assetName},
		{name: "checksum mismatch", corrupt: true, ask: assetName, wantErr: "does not match its checksum"},
		{name: "no such asset", ask: "deepcove_v1.2.3_plan9-386.tar.gz", wantErr: "has no"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{assetName: body}
			files[checksumsAsset] = checksumsFile(files)
			if tc.corrupt {
				files[assetName] = []byte("something else entirely")
			}
			server := mockReleaseServer(t, "v1.2.3", files, "Bearer secret")
			client := &Client{httpClient: server.Client(), api: server.URL, repo: "owner/repo", token: "secret"}

			release, err := client.Latest(context.Background())
			if err != nil {
				t.Fatalf("Latest: %v", err)
			}
			if release.Tag != "v1.2.3" {
				t.Fatalf("tag = %q", release.Tag)
			}

			path, err := client.Download(context.Background(), release, tc.ask, t.TempDir(), nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				if path != "" {
					t.Errorf("a rejected download left %s behind", path)
				}
				return
			}
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != string(body) {
				t.Errorf("downloaded %q, want %q", got, body)
			}
		})
	}
}

func TestClientByTagMissing(t *testing.T) {
	server := mockReleaseServer(t, "v1.2.3", map[string][]byte{}, "")
	client := &Client{httpClient: server.Client(), api: server.URL, repo: "owner/repo"}

	if _, err := client.ByTag(context.Background(), "v9.9.9"); err == nil {
		t.Fatal("wanted an error for a tag that does not exist")
	} else if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("a 404 should mention the token, got %v", err)
	}
}

func writeZip(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	zipWriter := zip.NewWriter(file)
	for inner, body := range files {
		entry, err := zipWriter.Create(inner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTarGz(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for inner, body := range files {
		header := &tar.Header{Name: inner, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractBinary(t *testing.T) {
	cases := []struct {
		name    string
		build   func(t *testing.T, dir string) string
		want    string
		wantErr bool
	}{
		{
			name: "zip",
			build: func(t *testing.T, dir string) string {
				return writeZip(t, dir, "release.zip", map[string]string{
					"deepcove": "the binary", "LICENSE": "text",
				})
			},
			want: "the binary",
		},
		{
			name: "tar.gz with a leading dot",
			build: func(t *testing.T, dir string) string {
				return writeTarGz(t, dir, "release.tar.gz", map[string]string{
					"./deepcove": "the binary", "./README.md": "text",
				})
			},
			want: "the binary",
		},
		{
			name: "archive without the binary",
			build: func(t *testing.T, dir string) string {
				return writeTarGz(t, dir, "empty.tar.gz", map[string]string{"./LICENSE": "text"})
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := tc.build(t, dir)
			path, err := ExtractBinary(archive, "deepcove", dir)
			if tc.wantErr {
				if err == nil {
					t.Fatal("wanted an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractBinary: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "deepcove")
	if err := os.WriteFile(target, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, ".deepcove-new")
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Replace(target, fresh); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v, want the mode the old binary had", info.Mode().Perm())
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Errorf("the replaced binary was left behind")
	}
}

func TestCanWrite(t *testing.T) {
	if !CanWrite(t.TempDir()) {
		t.Error("a temp dir should be writable")
	}
	if CanWrite(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("a missing dir is not writable")
	}
}

func TestDownloadReportsProgress(t *testing.T) {
	const assetName = "deepcove_v1.2.3_linux-amd64.tar.gz"
	body := []byte(strings.Repeat("payload", 1000))
	files := map[string][]byte{assetName: body}
	files[checksumsAsset] = checksumsFile(files)

	server := mockReleaseServer(t, "v1.2.3", files, "")
	client := &Client{httpClient: server.Client(), api: server.URL, repo: "owner/repo"}
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	var calls int
	var lastComplete, lastTotal int64
	_, err = client.Download(context.Background(), release, assetName, t.TempDir(),
		func(complete, total int64) {
			calls++
			lastComplete, lastTotal = complete, total
		})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if calls == 0 {
		t.Fatal("progress was never reported")
	}
	if lastComplete != int64(len(body)) {
		t.Errorf("last complete = %d, want %d", lastComplete, len(body))
	}
	if lastTotal != int64(len(body)) {
		t.Errorf("total = %d, want the content length %d", lastTotal, len(body))
	}
}
