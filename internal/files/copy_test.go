package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopy(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "from")
	if err := os.WriteFile(from, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		from    string
		to      string
		want    string
		wantErr bool
	}{
		{name: "to a new file", from: from, to: filepath.Join(dir, "new"), want: "new"},
		{name: "over a file already there", from: from, to: filepath.Join(dir, "old"), want: "new"},
		{name: "from a file that is not there", from: filepath.Join(dir, "missing"), to: filepath.Join(dir, "x"), wantErr: true},
	}
	if err := os.WriteFile(filepath.Join(dir, "old"), []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Copy(tc.from, tc.to)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("err %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			got, err := os.ReadFile(tc.to)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
