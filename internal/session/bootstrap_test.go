package session

import (
	"context"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestBootstrapStatusByRow(t *testing.T) {
	cases := []struct {
		name     string
		stop     bool
		inFlight bool
		want     BootstrapStatus
	}{
		{name: "a running session is asked", want: BootstrapInstalling},
		{name: "a session whose machine is restarting is asked", inFlight: true, want: BootstrapInstalling},
		{name: "a stopped session is not", stop: true, want: BootstrapReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newServiceWithRuntime(t, db.OpenForTest(t), newFakeSandbox())
			sess := mustCreate(t, svc, "claude")
			ctx := context.Background()
			if tc.stop {
				if err := svc.Stop(ctx, sess.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
			}
			if tc.inFlight {
				if _, ok := svc.beginStarting(sess.ID); !ok {
					t.Fatal("setup beginStarting refused")
				}
			}
			resp, err := svc.Bootstrap(ctx, sess.ID)
			if err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if resp.Status != tc.want {
				t.Errorf("status: got %q, want %q", resp.Status, tc.want)
			}
		})
	}
}
