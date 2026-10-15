//go:build windows

package localpeer

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"tailscale.com/net/netstat"
)

// sameUser finds the process holding the client's socket and compares its account with this one's. The
// same approach Tailscale used for its local API on Windows.
func sameUser(client, server netip.AddrPort) (bool, error) {
	table, err := netstat.Get()
	if err != nil {
		return false, fmt.Errorf("localpeer: %w", err)
	}
	for _, entry := range table.Entries {
		if entry.Local == client && entry.Remote == server {
			return processIsOurs(uint32(entry.Pid))
		}
	}
	return false, errors.New("localpeer: the client's socket is not in the TCP table")
}

func processIsOurs(pid uint32) (bool, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		// Another account's process cannot be opened like this, which is itself the answer.
		return false, nil
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return false, nil
	}
	defer func() { _ = token.Close() }()
	theirs, err := token.GetTokenUser()
	if err != nil {
		return false, fmt.Errorf("localpeer: %w", err)
	}
	ours, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, fmt.Errorf("localpeer: %w", err)
	}
	return windows.EqualSid(theirs.User.Sid, ours.User.Sid), nil
}
