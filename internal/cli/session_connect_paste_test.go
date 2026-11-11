package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/session"
)

func TestPasteCollector_Collect(t *testing.T) {
	longText := strings.Repeat("a", maxCollectedPaste)
	cases := []struct {
		name  string
		reads []string
		want  []inputPart
	}{
		{
			name:  "typing",
			reads: []string{"hello"},
			want:  []inputPart{{data: []byte("hello")}},
		},
		{
			name:  "paste in one read",
			reads: []string{"\x1b[200~/a b\x1b[201~"},
			want:  []inputPart{{data: []byte("\x1b[200~/a b\x1b[201~"), isPossiblyFilePaste: true}},
		},
		{
			name:  "paste over three reads, as Ghostty sends a dropped file",
			reads: []string{"\x1b[200~", "/a\\ b.png", "\x1b[201~"},
			want:  []inputPart{{data: []byte("\x1b[200~/a\\ b.png\x1b[201~"), isPossiblyFilePaste: true}},
		},
		{
			name:  "end marker split across reads",
			reads: []string{"\x1b[200~/a\x1b[2", "01~"},
			want:  []inputPart{{data: []byte("\x1b[200~/a\x1b[201~"), isPossiblyFilePaste: true}},
		},
		{
			name:  "typing around a paste",
			reads: []string{"x\x1b[200~/a\x1b[201~y"},
			want: []inputPart{
				{data: []byte("x")},
				{data: []byte("\x1b[200~/a\x1b[201~"), isPossiblyFilePaste: true},
				{data: []byte("y")},
			},
		},
		{
			name:  "two pastes in one read",
			reads: []string{"\x1b[200~/a\x1b[201~\x1b[200~/b\x1b[201~"},
			want: []inputPart{
				{data: []byte("\x1b[200~/a\x1b[201~"), isPossiblyFilePaste: true},
				{data: []byte("\x1b[200~/b\x1b[201~"), isPossiblyFilePaste: true},
			},
		},
		{
			name:  "long paste goes on as pasted text",
			reads: []string{"\x1b[200~" + longText, "b\x1b[201~"},
			want: []inputPart{
				{data: []byte("\x1b[200~" + longText[5:]), isConsideredTextPaste: true},
				{data: []byte("aaaaab\x1b[201~"), isConsideredTextPaste: true},
			},
		},
		{
			name:  "Ctrl+\\ byte inside a long paste stays pasted",
			reads: []string{"\x1b[200~" + longText, "\x1c\x1b[201~", "x"},
			want: []inputPart{
				{data: []byte("\x1b[200~" + longText[5:]), isConsideredTextPaste: true},
				{data: []byte("aaaaa\x1c\x1b[201~"), isConsideredTextPaste: true},
				{data: []byte("x")},
			},
		},
		{
			name:  "end marker split across reads of a long paste",
			reads: []string{"\x1b[200~" + longText + "\x1b[20", "1~y"},
			want: []inputPart{
				{data: []byte("\x1b[200~" + longText[1:]), isConsideredTextPaste: true},
				{data: []byte("a\x1b[201~"), isConsideredTextPaste: true},
				{data: []byte("y")},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pastes pasteCollector
			var got []inputPart
			for _, read := range tc.reads {
				got = append(got, pastes.collect([]byte(read))...)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("collect = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPasteCollector_Flush(t *testing.T) {
	cases := []struct {
		name  string
		reads []string
		want  []inputPart
	}{
		{name: "nothing held", reads: []string{"hello"}},
		{name: "paste that stopped short", reads: []string{"\x1b[200~/a.png\x1c"}, want: []inputPart{{data: []byte("\x1b[200~/a.png\x1c")}}},
		{name: "whole paste already handed on", reads: []string{"\x1b[200~/a.png\x1b[201~"}},
		{name: "long paste that stopped short", reads: []string{"\x1b[200~" + strings.Repeat("a", maxCollectedPaste)}, want: []inputPart{{data: []byte("aaaaa")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pastes pasteCollector
			for _, read := range tc.reads {
				pastes.collect([]byte(read))
			}
			got := pastes.flush()
			if !reflect.DeepEqual(got, tc.want) || pastes.isPasteInProgress {
				t.Errorf("flush = %+v with isPasteInProgress %v, want %+v", got, pastes.isPasteInProgress, tc.want)
			}
		})
	}
}

func TestReadTerminal(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		isCancelled bool
		want        []string
	}{
		{name: "reads until the end", input: "abc", want: []string{"abc"}},
		{name: "nothing to read", input: ""},
		{name: "stops when cancelled", input: "abc", isCancelled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads := make(chan []byte)
			hasExited := make(chan struct{})
			go func() {
				readTerminal(ctx, strings.NewReader(tc.input), reads)
				close(hasExited)
			}()
			if tc.isCancelled {
				// Nothing takes the read, so the only way out is the cancel.
				cancel()
				<-hasExited
			}
			var got []string
			for read := range reads {
				got = append(got, string(read))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("readTerminal(%q) handed on %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestUploadWhileReading(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paste := []byte("\x1b[200~" + file + "\x1b[201~")
	cases := []struct {
		name      string
		typed     []string
		isDetach  bool
		wantPaste string
		wantParts []string
		wantError string
	}{
		{name: "nothing typed", wantPaste: "\x1b[200~/tmp/.uploads/a.png \x1b[201~"},
		{name: "typing waits for the paste", typed: []string{"a", "b"}, wantPaste: "\x1b[200~/tmp/.uploads/a.png \x1b[201~", wantParts: []string{"a", "b"}},
		{name: "the key inside a later paste is text", typed: []string{"\x1b[200~\x1c\x1b[201~"}, wantPaste: "\x1b[200~/tmp/.uploads/a.png \x1b[201~", wantParts: []string{"\x1b[200~\x1c\x1b[201~"}},
		{name: "detach after a paste that stopped short", typed: []string{"\x1b[200~/b.png", "\x1c"}, isDetach: true, wantParts: []string{"\x1b[200~/b.png\x1c"}},
		{name: "detach cancels the upload", typed: []string{"a", "\x1c"}, isDetach: true, wantParts: []string{"a", "\x1c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isStarted := make(chan struct{})
			isFinished := make(chan struct{})
			upload := func(ctx context.Context, localPath string) (string, error) {
				close(isStarted)
				select {
				case <-isFinished:
					return "/tmp/.uploads/" + filepath.Base(localPath), nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			reads := make(chan []byte)
			go func() {
				<-isStarted
				for _, key := range tc.typed {
					reads <- []byte(key)
				}
				if !tc.isDetach {
					close(isFinished)
				}
			}()
			var pastes pasteCollector
			got, typed, err := uploadWhileReading(t.Context(), paste, reads, &pastes, upload)
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			var gotParts []string
			for _, part := range typed {
				gotParts = append(gotParts, string(part.data))
			}
			if string(got) != tc.wantPaste || errText != tc.wantError || !reflect.DeepEqual(gotParts, tc.wantParts) {
				t.Errorf("uploadWhileReading = %q, %q, %q, want %q, %q, %q", got, gotParts, errText, tc.wantPaste, tc.wantParts, tc.wantError)
			}
		})
	}
}

func TestBuildShellEscapedPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "vm upload", path: "/tmp/.uploads/0a1b2c3d4e5f6a7b.png", want: "/tmp/.uploads/0a1b2c3d4e5f6a7b.png"},
		{name: "space", path: "/home/my user/.cache/a.png", want: `/home/my\ user/.cache/a.png`},
		{name: "shell characters", path: "/home/$x;(y)'z'/a.png", want: `/home/\$x\;\(y\)\'z\'/a.png`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildShellEscapedPath(tc.path); got != tc.want {
				t.Errorf("buildShellEscapedPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestBuildUploadedPaste(t *testing.T) {
	folder := t.TempDir()
	screenshot := filepath.Join(folder, "Screenshot at 10.49.36 PM.png")
	notes := filepath.Join(folder, "notes.txt")
	for _, path := range []string{screenshot, notes} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	escapedScreenshot := strings.ReplaceAll(screenshot, " ", `\ `)
	uploadedScreenshot := "/tmp/.uploads/" + strings.ReplaceAll(filepath.Base(screenshot), " ", `\ `)
	cases := []struct {
		name      string
		paste     string
		failing   []string
		want      string
		wantError string
	}{
		{
			name:  "dropped file, backslash escaped",
			paste: "\x1b[200~" + escapedScreenshot + "\x1b[201~",
			want:  "\x1b[200~" + uploadedScreenshot + " \x1b[201~",
		},
		{
			name:  "two files with a trailing space",
			paste: "\x1b[200~" + escapedScreenshot + " '" + notes + "' \x1b[201~",
			want:  "\x1b[200~" + uploadedScreenshot + " /tmp/.uploads/notes.txt \x1b[201~",
		},
		{
			name:  "text is left alone",
			paste: "\x1b[200~see " + notes + "\x1b[201~",
			want:  "\x1b[200~see " + notes + "\x1b[201~",
		},
		{
			name:      "failed upload pastes nothing",
			paste:     "\x1b[200~" + notes + "\x1b[201~",
			failing:   []string{notes},
			wantError: "could not upload notes.txt: session is not running",
		},
		{
			name:      "every failure is named with its reason",
			paste:     "\x1b[200~" + escapedScreenshot + " " + notes + "\x1b[201~",
			failing:   []string{screenshot, notes},
			wantError: "could not upload " + filepath.Base(screenshot) + ": session is not running, notes.txt: session is not running",
		},
		{
			name:      "one failed upload keeps the other",
			paste:     "\x1b[200~" + escapedScreenshot + " " + notes + "\x1b[201~",
			failing:   []string{notes},
			want:      "\x1b[200~" + uploadedScreenshot + " \x1b[201~",
			wantError: "could not upload notes.txt: session is not running",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upload := func(_ context.Context, localPath string) (string, error) {
				if slices.Contains(tc.failing, localPath) {
					return "", errors.New("session is not running")
				}
				return "/tmp/.uploads/" + filepath.Base(localPath), nil
			}
			got, err := buildUploadedPaste(t.Context(), []byte(tc.paste), upload)
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			if string(got) != tc.want || errText != tc.wantError {
				t.Errorf("buildUploadedPaste = %q, %q, want %q, %q", got, errText, tc.want, tc.wantError)
			}
		})
	}
}

func TestFindLocalFilePaths(t *testing.T) {
	folder := t.TempDir()
	file := filepath.Join(folder, "a.png")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		text string
		want []string
	}{
		{name: "file", text: file, want: []string{file}},
		{name: "missing file", text: file + " " + filepath.Join(folder, "gone.png")},
		{name: "folder", text: folder},
		{name: "relative path", text: "a.png"},
		{name: "empty", text: " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findLocalFilePaths(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findLocalFilePaths(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestSplitPastedWords(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{name: "Ghostty escapes spaces", text: `/a/Screenshot\ at\ 10.49.36` + " " + `PM.png`, want: []string{"/a/Screenshot at 10.49.36 PM.png"}},
		{name: "trailing space", text: "/a/b.png ", want: []string{"/a/b.png"}},
		{name: "single quotes", text: "'/a/my file.png'", want: []string{"/a/my file.png"}},
		{name: "double quotes", text: `"/a/my file.png"`, want: []string{"/a/my file.png"}},
		{name: "escaped quote in double quotes", text: `"/tmp/a\"b.png"`, want: []string{`/tmp/a"b.png`}},
		{name: "escaped backslash in double quotes", text: `"/tmp/a\\b.png"`, want: []string{`/tmp/a\b.png`}},
		{name: "other backslash in double quotes", text: `"/tmp/a\b.png"`, want: []string{`/tmp/a\b.png`}},
		{name: "backslash in single quotes", text: `'/tmp/a\b.png'`, want: []string{`/tmp/a\b.png`}},
		{name: "newline joined", text: "/a/b.png\n/a/c.png\n", want: []string{"/a/b.png", "/a/c.png"}},
		{name: "unfinished single quote", text: "'/a/b.png"},
		{name: "unfinished double quote", text: `"/a/b.png`},
		{name: "trailing backslash", text: `/a/b.png\`},
		{name: "trailing backslash in double quotes", text: `"/a/b.png\`},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitPastedWords(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitPastedWords(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestReadFileToUpload(t *testing.T) {
	folder := t.TempDir()
	small := filepath.Join(folder, "small.png")
	if err := os.WriteFile(small, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(folder, "big.mov")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, session.MaxUploadBytes+1); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		path      string
		want      string
		wantError string
	}{
		{name: "small file", path: small, want: "x"},
		{name: "too big", path: big, wantError: "bigger than 25 MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readFileToUpload(tc.path)
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			if string(got) != tc.want || errText != tc.wantError {
				t.Errorf("readFileToUpload(%q) = %q, %q, want %q, %q", tc.path, got, errText, tc.want, tc.wantError)
			}
		})
	}
}
