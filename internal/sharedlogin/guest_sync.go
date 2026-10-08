package sharedlogin

import (
	"errors"
)

// LoginFiles is how a guest reads and writes one agent's login, where that agent looks for it.
type LoginFiles interface {
	// Read returns the agent's login, empty when it has none, and whether first run setup is done.
	Read() (local Login, onboarded bool, err error)
	WriteTokens(login Login) error
	// WriteAccount records the account and marks first run setup done.
	WriteAccount(login Login) error
	// RemoveTokens logs the agent out.
	RemoveTokens() error
	// TokenPath is the file the agent needs at start.
	TokenPath() string
}

// LoginFilesFor says where an agent keeps its login under home, nil for an agent that cannot share one.
func LoginFilesFor(agent, home string) LoginFiles {
	switch agent {
	case "claude":
		return ClaudeLoginFiles{Home: home}
	}
	return nil
}

// GuestSync keeps one session's agent login in step with the user's shared one.
type GuestSync struct {
	Files LoginFiles
	Ask   func(Request) (Reply, error)
}

// Once runs one round. A login the agent made itself carries a refresh token, so it is offered to the
// host. Otherwise the session takes the shared login. Another account is left as it is.
func (g *GuestSync) Once() error {
	local, onboarded, err := g.Files.Read()
	if err != nil {
		return err
	}
	var request Request
	if local.RefreshToken != "" {
		request.Offer = &local
	}
	reply, err := g.Ask(request)
	switch {
	case err != nil:
		return err
	case reply.Error != "":
		return errors.New(reply.Error)
	case reply.NotSharing:
		// Sharing was turned off. A shared copy has no refresh token, so it goes and the agent asks to log in.
		if local.AccessToken != "" && local.RefreshToken == "" {
			return g.Files.RemoveTokens()
		}
		return nil
	case reply.DifferentAccount, reply.Login == nil:
		return nil
	}
	shared := *reply.Login
	// The session's own login is rewritten too, without its refresh token, now the host has it.
	if shared.AccessToken != local.AccessToken || local.RefreshToken != "" {
		if err := g.Files.WriteTokens(shared); err != nil {
			return err
		}
	}
	if shared.AccountID != local.AccountID || !onboarded {
		return g.Files.WriteAccount(shared)
	}
	return nil
}
