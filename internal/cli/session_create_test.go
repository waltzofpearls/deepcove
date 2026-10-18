package cli

import (
	"testing"

	"github.com/waltzofpearls/deepcove/internal/session"
)

func TestSharedFolderFlagSet(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    session.SharedFolderRequest
		wantErr bool
	}{
		{name: "read and write by default", value: "/code:/workspace", want: session.SharedFolderRequest{HostPath: "/code", GuestPath: "/workspace"}},
		{name: "rw said out loud", value: "/code:/workspace:rw", want: session.SharedFolderRequest{HostPath: "/code", GuestPath: "/workspace"}},
		{name: "read only", value: "/data:/seed:ro", want: session.SharedFolderRequest{HostPath: "/data", GuestPath: "/seed", ReadOnly: true}},
		{name: "a guest path is required", value: "/code", wantErr: true},
		{name: "an unknown third part is refused", value: "/code:/workspace:readonly", wantErr: true},
		{name: "too many parts", value: "/a:/b:ro:extra", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var flag sharedFolderFlag
			err := flag.Set(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want an error", flag.values)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(flag.values) != 1 || flag.values[0] != tc.want {
				t.Errorf("got %+v, want %+v", flag.values, tc.want)
			}
		})
	}
}
