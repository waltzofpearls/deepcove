package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// SetSharedLogins serves keeper's logins to the UI and has keeper ask which sessions share. store holds
// the node's login settings.
func (s *Service) SetSharedLogins(keeper *sharedlogin.Keeper, store *SharedLoginStore) {
	s.sharedLogins, s.sharedLoginStore = keeper, store
	keeper.SetSharingCheck(s.sharesLogin)
}

// sharedLoginView is the shared login plus this node's login settings, for the UI. The API key itself
// never leaves the node.
type sharedLoginView struct {
	sharedlogin.Status
	DefaultLoginMode LoginMode `json:"default_login_mode"`
	HasAPIKey        bool      `json:"has_api_key"`
	APIKeyEnding     string    `json:"api_key_ending"`
}

func (s *Service) sharedLoginViewFor(agent string) (sharedLoginView, error) {
	status, err := s.sharedLogins.Status(agent)
	if err != nil {
		return sharedLoginView{}, err
	}
	mode, err := s.sharedLoginStore.DefaultLoginMode(agent)
	if err != nil {
		return sharedLoginView{}, err
	}
	key, err := s.sharedLoginStore.APIKey(agent)
	if err != nil {
		return sharedLoginView{}, err
	}
	view := sharedLoginView{Status: status, DefaultLoginMode: mode, HasAPIKey: key != ""}
	if len(key) > 4 {
		view.APIKeyEnding = key[len(key)-4:]
	}
	return view, nil
}

// handleGetSharedLogin serves GET /api/shared-logins/{agent}.
func (s *Service) handleGetSharedLogin(w http.ResponseWriter, r *http.Request) {
	if s.sharedLogins == nil {
		writeErrorCode(w, http.StatusNotFound, codeNotFound, "this node shares no logins")
		return
	}
	view, err := s.sharedLoginViewFor(r.PathValue("agent"))
	if err != nil {
		writeErrorCode(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSaveLoginSettings serves PUT /api/shared-logins/{agent} with "default_login_mode" and "api_key".
// An empty api_key removes the saved one.
func (s *Service) handleSaveLoginSettings(w http.ResponseWriter, r *http.Request) {
	if s.sharedLogins == nil {
		writeErrorCode(w, http.StatusNotFound, codeNotFound, "this node shares no logins")
		return
	}
	agentName := r.PathValue("agent")
	agent, err := resolveAgent(agentName)
	if err != nil || !s.sharedLogins.CanShare(agentName) {
		writeErrorCode(w, http.StatusNotFound, codeNotFound, agentName+" has no login settings")
		return
	}
	var req struct {
		DefaultLoginMode *LoginMode `json:"default_login_mode"`
		APIKey           *string    `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DefaultLoginMode != nil && !slices.Contains(agent.LoginModes, *req.DefaultLoginMode) {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, fmt.Sprintf("%s cannot log in with %q", agentName, *req.DefaultLoginMode))
		return
	}
	if req.APIKey != nil {
		if err := s.sharedLoginStore.SetAPIKey(agentName, strings.TrimSpace(*req.APIKey)); err != nil {
			writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
			return
		}
	}
	if req.DefaultLoginMode != nil {
		if err := s.sharedLoginStore.SetDefaultLoginMode(agentName, *req.DefaultLoginMode); err != nil {
			writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
			return
		}
	}
	view, err := s.sharedLoginViewFor(agentName)
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSwitchSharedLogin serves POST /api/shared-logins/{agent}/switch.
func (s *Service) handleSwitchSharedLogin(w http.ResponseWriter, r *http.Request) {
	s.answerWaitingLogin(w, r, (*sharedlogin.Keeper).SwitchToWaitingLogin)
}

// handleKeepSharedLogin serves POST /api/shared-logins/{agent}/keep.
func (s *Service) handleKeepSharedLogin(w http.ResponseWriter, r *http.Request) {
	s.answerWaitingLogin(w, r, func(keeper *sharedlogin.Keeper, _ context.Context, agent string) error {
		return keeper.KeepSharedLogin(agent)
	})
}

func (s *Service) answerWaitingLogin(w http.ResponseWriter, r *http.Request, answer func(*sharedlogin.Keeper, context.Context, string) error) {
	if s.sharedLogins == nil {
		writeErrorCode(w, http.StatusNotFound, codeNotFound, "this node shares no logins")
		return
	}
	agent := r.PathValue("agent")
	err := answer(s.sharedLogins, r.Context(), agent)
	if errors.Is(err, sharedlogin.ErrNoWaitingLogin) {
		// Another tab or the Settings page answered first.
		writeErrorCode(w, http.StatusConflict, codeConflict, "That choice was already made.")
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	view, err := s.sharedLoginViewFor(agent)
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}
