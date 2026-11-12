package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/session"
)

func TestFindDetachKey(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		wantInput    string
		wantIsDetach bool
	}{
		{name: "typing", input: "hello", wantInput: "hello"},
		{name: "detach key alone", input: "\x1c", wantIsDetach: true},
		{name: "typed before the key", input: "ls\x1c", wantInput: "ls", wantIsDetach: true},
		{name: "typed after the key is dropped", input: "a\x1cb", wantInput: "a", wantIsDetach: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, isDetach := findDetachKey([]byte(tc.input))
			if string(input) != tc.wantInput || isDetach != tc.wantIsDetach {
				t.Errorf("findDetachKey(%q) = %q, %v, want %q, %v", tc.input, input, isDetach, tc.wantInput, tc.wantIsDetach)
			}
		})
	}
}

func TestBuildInputPieces(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  [][]byte
	}{
		{name: "empty"},
		{name: "one key", input: []byte("a"), want: [][]byte{[]byte("a")}},
		{name: "exactly one piece", input: bytes.Repeat([]byte("a"), maxInputPiece), want: [][]byte{bytes.Repeat([]byte("a"), maxInputPiece)}},
		{name: "big paste in order", input: append(bytes.Repeat([]byte("a"), 2*maxInputPiece), 'b'), want: [][]byte{bytes.Repeat([]byte("a"), maxInputPiece), bytes.Repeat([]byte("a"), maxInputPiece), []byte("b")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildInputPieces(tc.input); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildInputPieces got %d pieces, want %d", len(got), len(tc.want))
			}
		})
	}
}

func TestFindSessionMatches(t *testing.T) {
	apiOnMac := &session.Session{ID: "a1b2c3d4e5f6a7b8", Name: "api"}
	apiOnDroplet := &session.Session{ID: "a1b2f6a7b8c9d0e1", Name: "api"}
	web := &session.Session{ID: "e5f6a7b8c9d0e1f2", Name: "web"}
	sessions := []*session.Session{apiOnMac, apiOnDroplet, web}
	cases := []struct {
		name      string
		target    string
		want      []*session.Session
		wantTitle string
	}{
		{name: "full id", target: "a1b2c3d4e5f6a7b8", want: []*session.Session{apiOnMac}},
		{name: "unique name", target: "web", want: []*session.Session{web}},
		{name: "duplicate name", target: "api", want: []*session.Session{apiOnMac, apiOnDroplet}, wantTitle: `2 sessions are named "api". Which one?`},
		{name: "unique prefix", target: "e5f6", want: []*session.Session{web}},
		{name: "shared prefix", target: "a1b2", want: []*session.Session{apiOnMac, apiOnDroplet}, wantTitle: `2 sessions start with "a1b2". Which one?`},
		{name: "prefix too short", target: "a1b"},
		{name: "no match", target: "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, title := findSessionMatches(sessions, tc.target)
			if !reflect.DeepEqual(got, tc.want) || title != tc.wantTitle {
				t.Errorf("findSessionMatches(%q) = %v, %q, want %v, %q", tc.target, got, title, tc.want, tc.wantTitle)
			}
		})
	}
}

func TestBuildSessionRows(t *testing.T) {
	twoHoursAgo := time.Now().Add(-2*time.Hour - time.Minute)
	sessions := []*session.Session{
		{ID: "a1b2c3d4e5f6a7b8", Name: "api", Agent: "claude", Status: session.StatusRunning, CreatedAt: twoHoursAgo, Host: &session.NodeRef{Hostname: "macbook"}},
		{ID: "e5f6a7b8c9d0e1f2", Name: "web", Agent: "codex", Status: session.StatusStopped, CreatedAt: twoHoursAgo},
	}
	wantHeader := "ID            NAME  NODE       AGENT   STATUS   AGE"
	wantRows := []string{
		"a1b2c3d4e5f6  api   macbook    claude  running  2h",
		"e5f6a7b8c9d0  web   this node  codex   stopped  2h",
	}
	header, rows := buildSessionRows(sessions)
	if header != wantHeader || !reflect.DeepEqual(rows, wantRows) {
		t.Errorf("buildSessionRows = %q, %q, want %q, %q", header, rows, wantHeader, wantRows)
	}
}

func TestBuildUniqueIDs(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want []string
	}{
		{name: "different at 12", ids: []string{"a1b2c3d4e5f6a7b8", "e5f6a7b8c9d0e1f2"}, want: []string{"a1b2c3d4e5f6", "e5f6a7b8c9d0"}},
		{name: "same first 12", ids: []string{"a1b2c3d4e5f6a7b8", "a1b2c3d4e5f6c9d0"}, want: []string{"a1b2c3d4e5f6a", "a1b2c3d4e5f6c"}},
		{name: "same first 14", ids: []string{"a1b2c3d4e5f6a7b8", "a1b2c3d4e5f6a7c9", "e5f6a7b8c9d0e1f2"}, want: []string{"a1b2c3d4e5f6a7b", "a1b2c3d4e5f6a7c", "e5f6a7b8c9d0e1f"}},
		{name: "shorter than 12", ids: []string{"a1b2", "e5f6"}, want: []string{"a1b2", "e5f6"}},
		{name: "the same id twice", ids: []string{"a1b2c3d4e5f6a7b8", "a1b2c3d4e5f6a7b8"}, want: []string{"a1b2c3d4e5f6a7b8", "a1b2c3d4e5f6a7b8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions := make([]*session.Session, 0, len(tc.ids))
			for _, id := range tc.ids {
				sessions = append(sessions, &session.Session{ID: id})
			}
			if got := buildUniqueIDs(sessions); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildUniqueIDs(%q) = %q, want %q", tc.ids, got, tc.want)
			}
		})
	}
}

func TestHasDetachKey(t *testing.T) {
	cases := []struct {
		name  string
		parts []inputPart
		want  bool
	}{
		{name: "nothing"},
		{name: "typing", parts: []inputPart{{data: []byte("ls")}}},
		{name: "typed", parts: []inputPart{{data: []byte("ls")}, {data: []byte("\x1c")}}, want: true},
		{name: "inside a paste", parts: []inputPart{{data: []byte("\x1b[200~\x1c\x1b[201~"), isPossiblyFilePaste: true}}},
		{name: "inside a long paste", parts: []inputPart{{data: []byte("a\x1ca"), isConsideredTextPaste: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasDetachKey(tc.parts); got != tc.want {
				t.Errorf("hasDetachKey(%+v) = %v, want %v", tc.parts, got, tc.want)
			}
		})
	}
}

func TestBuildShortNotice(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "short", text: "could not upload a.png", want: "could not upload a.png"},
		{name: "exactly the limit", text: strings.Repeat("a", maxNoticeBytes), want: strings.Repeat("a", maxNoticeBytes)},
		{name: "too long", text: strings.Repeat("a", maxNoticeBytes+1), want: strings.Repeat("a", maxNoticeBytes-3) + "..."},
		{name: "cut inside a character", text: strings.Repeat("a", maxNoticeBytes-4) + "é" + strings.Repeat("a", 10), want: strings.Repeat("a", maxNoticeBytes-4) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildShortNotice(tc.text); got != tc.want {
				t.Errorf("buildShortNotice got %d bytes ending %q, want %d bytes", len(got), got[max(0, len(got)-8):], len(tc.want))
			}
		})
	}
}

func TestBuildOneLineName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "api", want: "api"},
		{name: "newline and tab", input: "api\nworker\tx", want: `api\nworker\tx`},
		{name: "escape character", input: "a\x1b[31mb", want: `a\x1b[31mb`},
		{name: "unicode", input: "café", want: "café"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildOneLineName(tc.input); got != tc.want {
				t.Errorf("buildOneLineName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
