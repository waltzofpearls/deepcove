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

// keepSharedLogins syncs each agent's login once before any agent starts, then in the background. No
// agents means every agent logs in on its own.
func keepSharedLogins(agents []string) {
	var syncs []*sharedlogin.GuestSync
	for _, agent := range agents {
		if files := sharedlogin.LoginFilesFor(agent, sandbox.SandboxedAgentHome); files != nil {
			syncs = append(syncs, &sharedlogin.GuestSync{Agent: agent, Files: files, Ask: askHostForLogin})
		}
	}
	lastProblems := map[string]string{}
	report := func(agent string, err error) {
		// Said once, not every 10 seconds while the host stays unreachable.
		if err != nil && err.Error() != lastProblems[agent] {
			logf("WARNING: shared login for %s: %v", agent, err)
		}
		lastProblems[agent] = fmt.Sprint(err)
	}
	syncAll := func() {
		for _, sync := range syncs {
			report(sync.Agent, sync.Once())
		}
	}
	if len(syncs) == 0 {
		return
	}
	syncAll()
	go func() {
		for {
			time.Sleep(sharedLoginCheckEvery)
			syncAll()
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
