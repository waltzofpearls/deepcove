// Package ghrelease downloads files from this repository's GitHub releases and checks them against the
// release's own SHA256SUMS.
//
// Used for the deepcove binary itself and for the VM runtime artifacts that ride along with it, so both
// take the same path and the same token while the repository is private.
package ghrelease

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	defaultRepo = "waltzofpearls/deepcove"
	defaultAPI  = "https://api.github.com"
)

// checksumsAsset is the file every release carries listing the sha256 of each archive.
const checksumsAsset = "SHA256SUMS"

// releaseAsset is one file attached to a release. URL is the API address rather than the browser one,
// because only the API address accepts a token, which is what a private repository needs.
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Release is the part of GitHub's release object we use.
type Release struct {
	Tag    string         `json:"tag_name"`
	Assets []releaseAsset `json:"assets"`
}

// asset returns the named file attached to this release.
func (r Release) asset(name string) (releaseAsset, bool) {
	for _, candidate := range r.Assets {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return releaseAsset{}, false
}

// Client reads releases and downloads their files.
type Client struct {
	httpClient *http.Client
	api        string
	repo       string
	token      string
}

// New builds a client for the real GitHub. The token is optional and only needed while the repository
// is private.
func New() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Minute},
		api:        defaultAPI,
		repo:       defaultRepo,
		token:      firstEnv("GITHUB_TOKEN", "GH_TOKEN"),
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// Latest is the newest release that is not a draft or a prerelease.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	return c.release(ctx, "releases/latest")
}

// ForVersion is the release a build takes its files from: its own, so a binary and the files it shipped
// with are always a matched set. A build from a working copy falls back to the newest release: its
// version is "dev", or a git describe such as v0.1.1-rc2-19-g51bb3af-dirty that names no release.
func (c *Client) ForVersion(ctx context.Context, version string) (Release, error) {
	if releaseTag.MatchString(version) {
		return c.ByTag(ctx, version)
	}
	return c.Latest(ctx)
}

// releaseTag is the shape of a tag a release is cut from, such as v1.2.3 or v1.2.3-rc2.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+(-rc\d+)?$`)

// ByTag is one exact release, which is how a named version is installed.
func (c *Client) ByTag(ctx context.Context, tag string) (Release, error) {
	return c.release(ctx, "releases/tags/"+url.PathEscape(tag))
}

func (c *Client) release(ctx context.Context, apiPath string) (Release, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/%s", c.api, c.repo, apiPath), "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("upgrade: could not read the release: %w", err)
	}
	if rel.Tag == "" {
		return Release{}, errors.New("upgrade: the release has no tag")
	}
	return rel, nil
}

func (c *Client) get(ctx context.Context, addr, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upgrade: %s: %w", addr, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusNotFound {
			// GitHub answers 404 both for no such release and for no access, so the advice depends on
			// whether a token was sent at all.
			if c.token == "" {
				return nil, fmt.Errorf("upgrade: %s: not found, either there is no such release or a private repository needs GITHUB_TOKEN or GH_TOKEN", addr)
			}
			return nil, fmt.Errorf("upgrade: %s: not found, there is no such release", addr)
		}
		return nil, fmt.Errorf("upgrade: %s: %s", addr, resp.Status)
	}
	return resp, nil
}

// AssetName is what the release workflow called this platform's archive.

// Download writes the named asset into dir and checks it against the release's own SHA256SUMS. Nothing
// is returned unless the sum matches. The caller removes the file.
//
// dir is where the binary lives, so the last step of an upgrade is a rename within one filesystem.
// report is called as bytes arrive and may be nil. Its total is zero when the server did not say how
// big the file is.
func (c *Client) Download(ctx context.Context, rel Release, name, dir string, report func(complete, total int64)) (string, error) {
	found, ok := rel.asset(name)
	if !ok {
		return "", fmt.Errorf("upgrade: release %s has no %s", rel.Tag, name)
	}
	want, err := c.checksum(ctx, rel, name)
	if err != nil {
		return "", err
	}

	resp, err := c.get(ctx, found.URL, "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	// The asset name goes on the end so the temp file keeps the real extension, which makes a file
	// left behind by a crash identifiable.
	file, err := os.CreateTemp(dir, ".deepcove-download-*-"+name)
	if err != nil {
		return "", fmt.Errorf("upgrade: %w", err)
	}
	digest := sha256.New()
	watched := io.Writer(digest)
	if report != nil {
		watched = io.MultiWriter(digest, &progressWriter{total: max(resp.ContentLength, 0), report: report})
	}
	_, copyErr := io.Copy(file, io.TeeReader(resp.Body, watched))
	closeErr := file.Close()
	if err := firstError(copyErr, closeErr); err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("upgrade: could not save %s: %w", name, err)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != want {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("upgrade: %s does not match its checksum, wanted %s, got %s", name, want, got)
	}
	return file.Name(), nil
}

// progressWriter counts what has arrived and reports it, so the caller can draw a bar without this
// package knowing anything about terminals.
type progressWriter struct {
	total    int64
	complete int64
	report   func(complete, total int64)
}

func (w *progressWriter) Write(chunk []byte) (int, error) {
	w.complete += int64(len(chunk))
	w.report(w.complete, w.total)
	return len(chunk), nil
}

func (c *Client) checksum(ctx context.Context, rel Release, name string) (string, error) {
	found, ok := rel.asset(checksumsAsset)
	if !ok {
		return "", fmt.Errorf("upgrade: release %s has no %s, so nothing can be verified", rel.Tag, checksumsAsset)
	}
	resp, err := c.get(ctx, found.URL, "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("upgrade: could not read %s: %w", checksumsAsset, err)
	}
	return checksumFor(body, name)
}

// checksumFor reads one line of sha256sum output. The star marks a file read in binary mode.
func checksumFor(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("upgrade: %s does not list %s", checksumsAsset, name)
}

// ExtractBinary writes the named binary from the archive into dir and returns its path. The caller
// removes the file.
//
// The format comes from the first bytes rather than the name, so it cannot be fooled by whatever the
// caller happened to call the file.
func ExtractBinary(archivePath, name, dir string) (string, error) {
	open := openTarGz
	zipped, err := isZip(archivePath)
	if err != nil {
		return "", err
	}
	if zipped {
		open = openZip
	}
	source, closeSource, err := open(archivePath, name)
	if err != nil {
		return "", err
	}
	defer closeSource()

	newFile, err := os.CreateTemp(dir, ".deepcove-new-*")
	if err != nil {
		return "", fmt.Errorf("upgrade: %w", err)
	}
	_, copyErr := io.Copy(newFile, source)
	closeErr := newFile.Close()
	if err := firstError(copyErr, closeErr); err != nil {
		_ = os.Remove(newFile.Name())
		return "", fmt.Errorf("upgrade: could not unpack %s: %w", name, err)
	}
	return newFile.Name(), nil
}

func openZip(archivePath, name string) (io.Reader, func(), error) {
	zipReader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("upgrade: %s: %w", archivePath, err)
	}
	for _, entry := range zipReader.File {
		if path.Base(entry.Name) != name {
			continue
		}
		entryReader, err := entry.Open()
		if err != nil {
			_ = zipReader.Close()
			return nil, nil, fmt.Errorf("upgrade: %s: %w", archivePath, err)
		}
		return entryReader, func() {
			_ = entryReader.Close()
			_ = zipReader.Close()
		}, nil
	}
	_ = zipReader.Close()
	return nil, nil, fmt.Errorf("upgrade: %s holds no %s", archivePath, name)
}

func openTarGz(archivePath, name string) (io.Reader, func(), error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("upgrade: %w", err)
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("upgrade: %s: %w", archivePath, err)
	}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err != nil {
			_ = gzipReader.Close()
			_ = file.Close()
			if errors.Is(err, io.EOF) {
				return nil, nil, fmt.Errorf("upgrade: %s holds no %s", archivePath, name)
			}
			return nil, nil, fmt.Errorf("upgrade: %s: %w", archivePath, err)
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == name {
			return tarReader, func() {
				_ = gzipReader.Close()
				_ = file.Close()
			}, nil
		}
	}
}

// isZip reports whether the file starts with the PKZip magic number, which no gzip stream can.
func isZip(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("upgrade: %w", err)
	}
	defer func() { _ = file.Close() }()
	var magic [2]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return false, fmt.Errorf("upgrade: %s: %w", path, err)
	}
	return string(magic[:]) == "PK", nil
}

// Replace puts newPath at target, keeping the permissions target already had.
//
// The old binary is moved aside first, because Windows refuses to overwrite a running executable, and
// is put back if the second rename fails.

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
