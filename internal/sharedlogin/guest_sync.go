package sharedlogin

import (
	"errors"
	"net/http"
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

// loginFiles says, for each agent that can share its login, where it keeps that login under a home.
var loginFiles = map[string]func(home string) LoginFiles{
	"claude": func(home string) LoginFiles { return ClaudeLoginFiles{Home: home} },
	"codex":  func(home string) LoginFiles { return CodexLoginFiles{Home: home} },
}

// LoginFilesFor says where an agent keeps its login under home, nil for an agent that cannot share one.
func LoginFilesFor(agent, home string) LoginFiles {
	files, ok := loginFiles[agent]
	if !ok {
		return nil
	}
	return files(home)
}

// Providers are the vendors whose logins this build can keep, one per agent that can share its login.
func Providers(client *http.Client) map[string]Provider {
	return map[string]Provider{
		"claude": ClaudeProvider{Client: client, TokenURL: ClaudeTokenURL, ProfileURL: ClaudeProfileURL},
		"codex":  &CodexProvider{Client: client, TokenURL: CodexTokenURL, KeysURL: CodexKeysURL},
	}
}

// GuestSync keeps one agent's login in a session in step with the user's shared one.
type GuestSync struct {
	Agent string
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
	request := Request{Agent: g.Agent}
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
