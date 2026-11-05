package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLocalLogin(t *testing.T) {
	dataDir := t.TempDir()
	first, err := NewLocalLogin(dataDir, LocalURL("127.0.0.1:9000"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dataDir, browserKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	again, err := NewLocalLogin(dataDir, LocalURL("127.0.0.1:9000"))
	if err != nil {
		t.Fatal(err)
	}
	if again.cookieValue != first.cookieValue {
		t.Error("a restart should keep the same cookie, so browsers stay unlocked")
	}
	if first.cookieName != "deepcove_9000" {
		t.Errorf("cookie name = %q, want deepcove_9000", first.cookieName)
	}
}

func TestNewLocalLogin_DamagedKey(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, browserKeyFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalLogin(dataDir, LocalURL("127.0.0.1:9000")); err == nil {
		t.Error("a damaged key should be an error, not a silent new key")
	}
}

func newCode(t *testing.T, login *LocalLogin, query string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	login.HandleNewCode(recorder, httptest.NewRequest(http.MethodPost, "http://unix/unlock-code?"+query, nil))
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	unlockURL, err := url.Parse(body.URL)
	if err != nil {
		t.Fatal(err)
	}
	return unlockURL.Query().Get("code")
}

func TestLocalLogin_HandleNewCode(t *testing.T) {
	login, err := NewLocalLogin(t.TempDir(), LocalURL("0.0.0.0:9090"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	login.HandleNewCode(recorder, httptest.NewRequest(http.MethodPost, "http://unix/unlock-code", nil))
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.URL, "http://127.0.0.1:9090/unlock?code=") {
		t.Errorf("url = %q, want the port deepcove really listens on", body.URL)
	}
}

func TestLocalLogin_Unlock(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		reuse      bool
		wait       time.Duration
		wrongCode  bool
		wantCookie bool
	}{
		{name: "a fresh code", wantCookie: true},
		{name: "a code used twice", reuse: true},
		{name: "a code past its life", wait: UnlockCodeLife + time.Second},
		{name: "a made up code", wrongCode: true},
		{name: "a pasted link a little later", query: PasteQuery, wait: UnlockCodeLife + time.Second, wantCookie: true},
		{name: "a pasted link past its life", query: PasteQuery, wait: PastedUnlockCodeLife + time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			login := newTestLogin(t)
			clock := time.Now()
			login.now = func() time.Time {
				return clock
			}
			code := newCode(t, login, tc.query)
			if tc.wrongCode {
				code = "0123456789abcdef0123456789abcdef"
			}
			if tc.reuse {
				login.unlock(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, UnlockPath+"?code="+code, nil))
			}
			clock = clock.Add(tc.wait)

			recorder := httptest.NewRecorder()
			login.unlock(recorder, httptest.NewRequest(http.MethodGet, UnlockPath+"?code="+code, nil))
			if recorder.Code != http.StatusSeeOther {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusSeeOther)
			}
			gotCookie := len(recorder.Result().Cookies()) > 0
			if gotCookie != tc.wantCookie {
				t.Errorf("cookie set = %v, want %v", gotCookie, tc.wantCookie)
			}
		})
	}
}
