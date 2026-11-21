package session

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed agents/agent_state_read.sh
var agentStateReader string

// AgentState is what a running session's agent is doing, read from the agent itself whenever sessions
// are listed. Empty when nothing could say, such as an agent still starting.
type AgentState string

const (
	AgentStateWorking AgentState = "working"
	// AgentStateNeedsYou is an agent waiting on an answer from you, such as an approval.
	AgentStateNeedsYou AgentState = "needs_you"
	// AgentStateDone is a turn that finished since you last looked at the session.
	AgentStateDone  AgentState = "done"
	AgentStateIdle  AgentState = "idle"
	AgentStateError AgentState = "error"
)

// AgentStateSource is where deepcove reads what an agent is doing.
type AgentStateSource string

const (
	// AgentStateFromClaudeSessionFile is the session file Claude keeps for each process, plus the hooks in
	// claude_state.sh.
	AgentStateFromClaudeSessionFile AgentStateSource = "claude_session_file"
	// AgentStateFromEscapeSequence is the OSC 7501 escape sequence the agent prints with its state. tmux
	// drops it, so deepcove saves it from the pane.
	AgentStateFromEscapeSequence AgentStateSource = "escape_sequence"
)

// agentStateRead is what the agent said, before deepcove knows whether you have seen it.
type agentStateRead struct {
	state AgentState
	// detail is what the agent waits for, or what failed.
	detail string
	// finishedAt is when the last turn finished, on this machine's clock. Zero when none did.
	finishedAt time.Time
}

// agentStateCacheTTL lets every open tab share one read of each agent.
const agentStateCacheTTL = time.Second

// agentStateReadTimeout keeps a slow machine from holding up the whole list.
const agentStateReadTimeout = 1500 * time.Millisecond

type cachedAgentStateRead struct {
	readAt    time.Time
	stateRead agentStateRead
}

// reportAgentState fills in what each running session's agent is doing. A session you are looking at
// right now never shows done.
func (s *Service) reportAgentState(ctx context.Context, sessions []*Session) {
	var wg sync.WaitGroup
	for _, sess := range sessions {
		if sess.Status != StatusRunning {
			continue
		}
		wg.Go(func() {
			read := s.readAgentState(ctx, sess)
			sess.AgentState = read.state
			sess.AgentStateDetail = read.detail
			if read.state == AgentStateDone && (s.isSessionCurrentlyBeingViewed(sess.ID) || !read.finishedAt.After(sess.LastActivityAt)) {
				sess.AgentState = AgentStateIdle
			}
		})
	}
	wg.Wait()
}

func (s *Service) readAgentState(ctx context.Context, sess *Session) agentStateRead {
	agent, err := resolveAgent(sess.Agent)
	if err != nil || agent.AgentStateSource == "" {
		return agentStateRead{}
	}
	s.agentStateCache.mu.Lock()
	cached, ok := s.agentStateCache.bySession[sess.ID]
	s.agentStateCache.mu.Unlock()
	if ok && time.Since(cached.readAt) < agentStateCacheTTL {
		return cached.stateRead
	}

	readCtx, cancel := context.WithTimeout(ctx, agentStateReadTimeout)
	defer cancel()
	out, ok := s.execCapture(readCtx, sess, []string{"sh", "-c", agentStateReader, "sh", sess.ID, string(agent.AgentStateSource), sess.Agent})
	var read agentStateRead
	if ok {
		read = parseAgentState(string(out), agent.AgentStateSource, time.Now())
	}

	s.agentStateCache.mu.Lock()
	if s.agentStateCache.bySession == nil {
		s.agentStateCache.bySession = make(map[string]cachedAgentStateRead)
	}
	s.agentStateCache.bySession[sess.ID] = cachedAgentStateRead{readAt: time.Now(), stateRead: read}
	s.agentStateCache.mu.Unlock()
	return read
}

// parseAgentState reads what agent_state_read.sh printed. Its times are on the machine's clock, which
// can drift from this one, so each becomes an age first.
func parseAgentState(out string, source AgentStateSource, now time.Time) agentStateRead {
	var (
		machineNow                           int64
		sessionFile, escapeSequence, failure string
		doneAt, escapeSequenceAt             int64
		isAlive                              bool
	)
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		kind, rest, _ := strings.Cut(scanner.Text(), " ")
		switch kind {
		case "now":
			machineNow, _ = strconv.ParseInt(rest, 10, 64)
		case "session_file":
			sessionFile = rest
		case "done":
			doneAt, _ = strconv.ParseInt(rest, 10, 64)
		case "error":
			failure = rest
		case "escape_sequence":
			at, status, _ := strings.Cut(rest, " ")
			escapeSequenceAt, _ = strconv.ParseInt(at, 10, 64)
			escapeSequence = status
		case "alive":
			isAlive = true
		}
	}
	// Without the machine's clock, no time it printed can be placed on this one.
	if machineNow == 0 {
		return agentStateRead{}
	}
	onThisClock := func(machineTime int64) time.Time {
		if machineTime == 0 {
			return time.Time{}
		}
		return now.Add(-time.Duration(machineNow-machineTime) * time.Second)
	}
	switch source {
	case AgentStateFromClaudeSessionFile:
		return agentStateFromClaudeSessionFile(sessionFile, failure, onThisClock(doneAt))
	case AgentStateFromEscapeSequence:
		if !isAlive {
			return agentStateRead{}
		}
		return agentStateFromEscapeSequence(escapeSequence, onThisClock(escapeSequenceAt))
	}
	return agentStateRead{}
}

// agentStateFromClaudeSessionFile reads the session file of a live Claude process. Its status is busy,
// waiting, idle, or shell, which is a background command running after the turn ended. A failed turn
// reads as idle there, so the hooks in claude_state.sh are what tell a finished turn from a failed one.
func agentStateFromClaudeSessionFile(sessionFile, failure string, finishedAt time.Time) agentStateRead {
	var parsed struct {
		Status     string `json:"status"`
		WaitingFor string `json:"waitingFor"`
	}
	if sessionFile == "" || json.Unmarshal([]byte(sessionFile), &parsed) != nil {
		return agentStateRead{}
	}
	switch parsed.Status {
	case "busy":
		return agentStateRead{state: AgentStateWorking}
	case "waiting":
		return agentStateRead{state: AgentStateNeedsYou, detail: parsed.WaitingFor}
	case "idle", "shell":
		if failure != "" {
			var stopFailure struct {
				Message string `json:"last_assistant_message"`
			}
			_ = json.Unmarshal([]byte(failure), &stopFailure)
			return agentStateRead{state: AgentStateError, detail: stopFailure.Message}
		}
		if !finishedAt.IsZero() {
			return agentStateRead{state: AgentStateDone, finishedAt: finishedAt}
		}
		return agentStateRead{state: AgentStateIdle}
	}
	return agentStateRead{}
}

// agentStateFromEscapeSequence reads an OSC 7501 status, such as
// "state=blocked:app=codex:kind=permission". Fields are split by colons, and msg is base64.
func agentStateFromEscapeSequence(status string, changedAt time.Time) agentStateRead {
	fields := map[string]string{}
	for _, field := range strings.Split(status, ":") {
		name, value, _ := strings.Cut(field, "=")
		fields[name] = value
	}
	message, _ := base64.StdEncoding.DecodeString(fields["msg"])
	switch fields["state"] {
	case "working":
		return agentStateRead{state: AgentStateWorking}
	case "blocked":
		return agentStateRead{state: AgentStateNeedsYou, detail: fields["kind"]}
	case "done":
		return agentStateRead{state: AgentStateDone, finishedAt: changedAt}
	case "idle":
		return agentStateRead{state: AgentStateIdle}
	case "error":
		return agentStateRead{state: AgentStateError, detail: string(message)}
	}
	return agentStateRead{}
}

// trackCurrentSessionViewers counts one viewer of a session, such as a browser tab or deepcove connect,
// until untrack is called.
func (s *Service) trackCurrentSessionViewers(id string) (untrack func()) {
	s.sessionViewersCount.mu.Lock()
	defer s.sessionViewersCount.mu.Unlock()
	if s.sessionViewersCount.counts == nil {
		s.sessionViewersCount.counts = make(map[string]int)
	}
	s.sessionViewersCount.counts[id]++
	return func() {
		s.sessionViewersCount.mu.Lock()
		defer s.sessionViewersCount.mu.Unlock()
		s.sessionViewersCount.counts[id]--
		if s.sessionViewersCount.counts[id] == 0 {
			delete(s.sessionViewersCount.counts, id)
		}
	}
}

func (s *Service) isSessionCurrentlyBeingViewed(id string) bool {
	s.sessionViewersCount.mu.Lock()
	defer s.sessionViewersCount.mu.Unlock()
	return s.sessionViewersCount.counts[id] > 0
}

// saveEscapeSequenceCommand keeps the latest status the agent prints in its session's state folder.
// Each OSC ends at the next ESC, and an ending BEL is cut off.
func saveEscapeSequenceCommand(sessionID string) string {
	return `dir="${TMPDIR:-/tmp}/deepcove-` + sessionID + `"; mkdir -p "$dir"; ` +
		`exec awk -v out="$dir/escape_sequence" 'BEGIN { RS = "\033" } ` +
		`/^\]7501;/ { sub(/^\]7501;/, ""); sub(/\007.*/, ""); print > out; close(out) }'`
}
