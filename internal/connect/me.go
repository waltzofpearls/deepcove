package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/waltzofpearls/deepcove/internal/netguard"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

const (
	profilePictureMaxBytes = 1 << 20
	profilePictureKeepFor  = time.Hour
)

// profilePictureTypes are the picture formats this node passes on. Each is a plain raster image, which
// a browser shows without running anything.
var profilePictureTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

type callerKey struct{}

// withCaller keeps who sent r over the tailnet, as the owner gate found them.
func withCaller(r *http.Request, caller *tailnet.Identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerKey{}, caller))
}

// me is the person using the dashboard: whoever sent r over the tailnet, or on this machine's own front
// doors, where only its own user gets in, whoever this node is signed in as. Nil when it is neither.
func (m *Manager) me(r *http.Request) *tailnet.Identity {
	if caller, ok := r.Context().Value(callerKey{}).(*tailnet.Identity); ok && caller != nil {
		return caller
	}
	return m.Status().Owner
}

func (m *Manager) handleMe(w http.ResponseWriter, r *http.Request) {
	me := m.me(r)
	if me == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, me)
}

// handleMyProfilePicture sends the picture Tailscale lists for the person using the dashboard. It takes
// no address, so it can only ever fetch that one picture, never one a page names.
func (m *Manager) handleMyProfilePicture(w http.ResponseWriter, r *http.Request) {
	me := m.me(r)
	if me == nil || me.ProfilePicURL == "" {
		http.NotFound(w, r)
		return
	}
	contentType, body, err := m.profilePicture.get(r.Context(), me.ProfilePicURL)
	if err != nil {
		m.logger.Warn("could not fetch a profile picture", "err", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// The answer depends on who is asking, so the browser must ask each time. This node keeps the copy.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// profilePicture downloads a profile picture for the dashboard, so the page shows it from this node and
// never loads anything from another site. The hosted web node's policy refuses images from other sites,
// since that page holds a Tailscale node's keys.
type profilePicture struct {
	client *http.Client

	mu          sync.Mutex
	url         string
	contentType string
	body        []byte
	fetchedAt   time.Time
}

func newProfilePicture() *profilePicture {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: dialPublicOnly}
	return &profilePicture{client: &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" {
				return errors.New("profile picture: redirected away from https")
			}
			if len(via) >= 5 {
				return errors.New("profile picture: too many redirects")
			}
			return nil
		},
	}}
}

// dialPublicOnly lets the picture download connect only to the public internet: never this machine, its
// LAN or its tailnet. The picture's site picks where a redirect goes, and this checks the address really
// dialed, so neither a redirect nor a DNS answer can point the download at something inside.
func dialPublicOnly(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	hostNetworks, err := netguard.HostNetworks()
	if err != nil {
		return fmt.Errorf("profile picture: %w", err)
	}
	if netguard.Refuses(addrPort.Addr(), nil, hostNetworks) {
		return fmt.Errorf("profile picture: %s is not on the public internet", addrPort.Addr())
	}
	return nil
}

// get returns the picture at pictureURL, from the copy fetched within the last hour if there is one.
func (picture *profilePicture) get(ctx context.Context, pictureURL string) (contentType string, body []byte, err error) {
	picture.mu.Lock()
	defer picture.mu.Unlock()
	if picture.url == pictureURL && time.Since(picture.fetchedAt) < profilePictureKeepFor {
		return picture.contentType, picture.body, nil
	}
	contentType, body, err = picture.fetch(ctx, pictureURL)
	if err != nil {
		return "", nil, err
	}
	picture.url, picture.contentType, picture.body, picture.fetchedAt = pictureURL, contentType, body, time.Now()
	return contentType, body, nil
}

func (picture *profilePicture) fetch(ctx context.Context, pictureURL string) (string, []byte, error) {
	parsed, err := url.Parse(pictureURL)
	if err != nil || parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("profile picture: %q is not an https address", pictureURL)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pictureURL, nil)
	if err != nil {
		return "", nil, err
	}
	response, err := picture.client.Do(request)
	if err != nil {
		return "", nil, fmt.Errorf("profile picture: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("profile picture: got %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, profilePictureMaxBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("profile picture: %w", err)
	}
	if len(body) > profilePictureMaxBytes {
		return "", nil, errors.New("profile picture: bigger than 1 MB")
	}
	// Go by what the bytes are, not by what the other site says they are.
	contentType := http.DetectContentType(body)
	if !profilePictureTypes[contentType] {
		return "", nil, fmt.Errorf("profile picture: %s is not a picture we pass on", contentType)
	}
	return contentType, body, nil
}
