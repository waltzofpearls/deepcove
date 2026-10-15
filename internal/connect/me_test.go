package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// pngHeader is enough of a PNG for http.DetectContentType to call it one.
var pngHeader = []byte("\x89PNG\r\n\x1a\n")

func TestHandleMe(t *testing.T) {
	caller := &tailnet.Identity{UserID: 7, LoginName: "ana@example.com", DisplayName: "Ana Lima"}
	cases := []struct {
		name       string
		caller     *tailnet.Identity
		wantStatus int
		wantLogin  string
	}{
		{"someone over the tailnet", caller, http.StatusOK, "ana@example.com"},
		{"this machine's own user, on a node with no tailnet", nil, http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := &Manager{logger: slog.Default()}
			request := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			if tc.caller != nil {
				request = withCaller(request, tc.caller)
			}
			recorder := httptest.NewRecorder()
			manager.handleMe(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if tc.wantLogin == "" {
				return
			}
			var me tailnet.Identity
			if err := json.Unmarshal(recorder.Body.Bytes(), &me); err != nil || me.LoginName != tc.wantLogin {
				t.Errorf("me = %+v, %v, want login %q", me, err, tc.wantLogin)
			}
		})
	}
}

func TestProfilePictureGet(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/picture.png":
			_, _ = w.Write(pngHeader)
		case "/page.html":
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/huge.png":
			_, _ = w.Write(append(pngHeader, bytes.Repeat([]byte{0}, profilePictureMaxBytes)...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cases := []struct {
		name            string
		url             string
		wantContentType string
		wantErr         string
	}{
		{name: "a picture", url: server.URL + "/picture.png", wantContentType: "image/png"},
		{name: "a page that is not a picture", url: server.URL + "/page.html", wantErr: "not a picture"},
		{name: "a picture over 1 MB", url: server.URL + "/huge.png", wantErr: "bigger than 1 MB"},
		{name: "no picture there", url: server.URL + "/gone.png", wantErr: "404"},
		{name: "plain http", url: strings.Replace(server.URL, "https://", "http://", 1) + "/picture.png", wantErr: "not an https address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			picture := &profilePicture{client: server.Client()}
			contentType, _, err := picture.get(context.Background(), tc.url)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || contentType != tc.wantContentType {
				t.Errorf("get = %q, %v, want %q", contentType, err, tc.wantContentType)
			}
		})
	}
}

func TestDialPublicOnly(t *testing.T) {
	cases := []struct {
		address string
		wantErr bool
	}{
		{"1.1.1.1:443", false},
		{"[2606:4700:4700::1111]:443", false},
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"10.0.0.5:443", true},
		{"192.168.1.10:443", true},
		{"100.101.102.103:443", true},
		{"[fd7a:115c:a1e0::1]:443", true},
		{"169.254.169.254:443", true},
		{"[fe80::1%en0]:443", true},
		{"[::1%lo0]:443", true},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			if err := dialPublicOnly("tcp", tc.address, nil); (err != nil) != tc.wantErr {
				t.Errorf("dialPublicOnly = %v, want an error: %v", err, tc.wantErr)
			}
		})
	}
}

func TestProfilePictureGet_RefusesThisMachine(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(pngHeader)
	}))
	defer server.Close()

	_, _, err := newProfilePicture().get(context.Background(), server.URL+"/picture.png")
	if err == nil || !strings.Contains(err.Error(), "not on the public internet") {
		t.Errorf("err %v, want the download refused for a server on this machine", err)
	}
}

func TestProfilePictureGet_KeepsACopy(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(pngHeader)
	}))
	defer server.Close()

	picture := &profilePicture{client: server.Client()}
	for range 3 {
		if _, _, err := picture.get(context.Background(), server.URL+"/picture.png"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := picture.get(context.Background(), server.URL+"/new-picture.png"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("the picture site was asked %d times, want 2: once per address", got)
	}
}
