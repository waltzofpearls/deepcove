//go:build linux

package guestinit

import (
	"fmt"
	"time"

	"github.com/mdlayher/vsock"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// sharedLoginCheckEvery is how often the guest asks for a newer shared login.
const sharedLoginCheckEvery = 10 * time.Second

// sharedLoginAskTimeout keeps the first ask inside the 20 seconds the host gives a booting VM.
const sharedLoginAskTimeout = 8 * time.Second

// keepSharedLogin syncs the agent's login once before the agent starts, then in the background. An empty
// agent means the session logs in on its own.
func keepSharedLogin(agent string) {
	files := sharedlogin.LoginFilesFor(agent, sandbox.SandboxedAgentHome)
	if files == nil {
		return
	}
	sync := &sharedlogin.GuestSync{Files: files, Ask: askHostForLogin}
	var lastProblem string
	report := func(err error) {
		// Said once, not every 10 seconds while the host stays unreachable.
		if err != nil && err.Error() != lastProblem {
			logf("WARNING: shared login: %v", err)
		}
		lastProblem = fmt.Sprint(err)
	}
	report(sync.Once())
	go func() {
		for {
			time.Sleep(sharedLoginCheckEvery)
			report(sync.Once())
		}
	}()
}

func askHostForLogin(request sharedlogin.Request) (sharedlogin.Reply, error) {
	conn, err := vsock.Dial(hostContextID, sharedlogin.Port, nil)
	if err != nil {
		return sharedlogin.Reply{}, fmt.Errorf("reach the host: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// A connection that cannot take a deadline still works, just without the bound.
	_ = conn.SetDeadline(time.Now().Add(sharedLoginAskTimeout))
	return sharedlogin.Ask(conn, request)
}
