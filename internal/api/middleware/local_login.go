package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UnlockPath is where a browser trades a one time code for the cookie. Only the local listener serves it.
const UnlockPath = "/unlock"

// LockedCode tags the 401 a locked browser gets, so the dashboard shows the locked screen.
const LockedCode = "browser_locked"

// UnlockCodeLife is how long the code in an unlock link works: long enough for a browser to open, short
// enough that a copy of the link is soon useless. Syncthing's `syncthing browser` uses the same 10 seconds.
const UnlockCodeLife = 10 * time.Second

// PastedUnlockCodeLife is for a link someone copies into another browser by hand, which takes longer.
const PastedUnlockCodeLife = time.Minute

// PasteQuery asks HandleNewCode for a link that works for PastedUnlockCodeLife.
const PasteQuery = "paste"

const (
	browserKeyFile = "browser_key"
	browserKeySize = 32
	// Chrome caps a cookie at 400 days. Loading the page renews it.
	cookieLife = 400 * 24 * time.Hour
)

// LocalLogin decides which browsers on this machine get in. `deepcove open` asks for a one time code over
// the Unix socket, which only this user can reach, and the browser trades it at /unlock for a cookie. The
// cookie is derived from a key kept in the data dir, so it survives restarts and upgrades. Deleting the
// key and restarting signs every browser out.
type LocalLogin struct {
	localURL    *url.URL
	cookieName  string
	cookieValue string
	now         func() time.Time

	mu    sync.Mutex
	codes map[string]time.Time
}

// NewLocalLogin loads the browser key from dataDir, making one the first time. localURL is where the
// unlock links point. The cookie is named after its port, so two deepcoves on one machine keep separate
// cookies.
func NewLocalLogin(dataDir string, localURL *url.URL) (*LocalLogin, error) {
	key, err := loadBrowserKey(dataDir)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("deepcove browser cookie"))
	return &LocalLogin{
		localURL:    localURL,
		cookieName:  "deepcove_" + localURL.Port(),
		cookieValue: hex.EncodeToString(mac.Sum(nil)),
		now:         time.Now,
		codes:       map[string]time.Time{},
	}, nil
}

func loadBrowserKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, browserKeyFile)
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return writeBrowserKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("local login: %w", err)
	}
	if len(key) != browserKeySize {
		return nil, fmt.Errorf("local login: %s is damaged. Delete it and restart deepcove", path)
	}
	return key, nil
}

func writeBrowserKey(path string) ([]byte, error) {
	key := make([]byte, browserKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("local login: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("local login: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("local login: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("local login: %w", err)
	}
	return key, nil
}

// HandleNewCode answers `deepcove open` with an unlock link holding a code that works once, within
// UnlockCodeLife, or PastedUnlockCodeLife when asked with PasteQuery. The link comes from the running
// deepcove, so it has the port deepcove really listens on. Serve it on the Unix socket only.
func (login *LocalLogin) HandleNewCode(w http.ResponseWriter, r *http.Request) {
	life := UnlockCodeLife
	if r.URL.Query().Has(PasteQuery) {
		life = PastedUnlockCodeLife
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	code := hex.EncodeToString(raw)
	login.mu.Lock()
	now := login.now()
	for oldCode, expires := range login.codes {
		if now.After(expires) {
			delete(login.codes, oldCode)
		}
	}
	login.codes[code] = now.Add(life)
	login.mu.Unlock()
	unlockURL := login.localURL.JoinPath(UnlockPath)
	unlockURL.RawQuery = "code=" + code
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": unlockURL.String()})
}

// unlock sets the cookie when the code is good, then sends the browser to the dashboard either way. A
// browser with a stale code lands on the locked screen, which says what to run.
func (login *LocalLogin) unlock(w http.ResponseWriter, r *http.Request) {
	if login.isCodeValid(r.URL.Query().Get("code")) {
		login.setCookie(w)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// isCodeValid also uses the code up, so each code works once.
func (login *LocalLogin) isCodeValid(code string) bool {
	login.mu.Lock()
	defer login.mu.Unlock()
	expires, ok := login.codes[code]
	delete(login.codes, code)
	return ok && !login.now().After(expires)
}

func (login *LocalLogin) hasSignedIn(r *http.Request) bool {
	cookie, err := r.Cookie(login.cookieName)
	return err == nil && hmac.Equal([]byte(cookie.Value), []byte(login.cookieValue))
}

func (login *LocalLogin) setCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     login.cookieName,
		Value:    login.cookieValue,
		Path:     "/",
		MaxAge:   int(cookieLife.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeResponseLocked(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "this browser is locked. Run `deepcove open` to unlock it",
		"code":  LockedCode,
	})
}
