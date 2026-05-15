package session

import (
	"fmt"
	"sort"
)

// AgentInfo is the public shape returned by GET /api/agents. The
// SPA's "new session" form populates its agent dropdown from this
// list; the JSON tags are the wire contract.
type AgentInfo struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// Agents returns the list of agent shorthands the service knows
// how to launch, sorted by name for stable output.
func Agents() []AgentInfo {
	out := make([]AgentInfo, 0, len(agentImages))
	for name, image := range agentImages {
		out = append(out, AgentInfo{Name: name, Image: image})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// agentImages is the v0 agent-name → OCI image map. Hardcoded by
// design — the SPEC §8.1 promise is that `deepcove session <agent>`
// resolves a short name internally. When we have more agents we'll
// move this to config (file or DB), but a map keeps the abstraction
// honest with zero ceremony in the meantime.
//
// The images themselves are placeholders for now. Real images will
// pin a known agent CLI + tmux + a sane shell environment.
var agentImages = map[string]string{
	"claude": "alpine:3.20", // TODO: real claude-code image
	"codex":  "alpine:3.20", // TODO: real codex image
	"gemini": "alpine:3.20", // TODO: real gemini image
}

// resolveImage looks up the OCI image for an agent shorthand. Returns
// a clear error for unknown agents so the CLI / API can surface them.
func resolveImage(agent string) (string, error) {
	img, ok := agentImages[agent]
	if !ok {
		return "", fmt.Errorf("unknown agent %q (known: claude, codex, gemini)", agent)
	}
	return img, nil
}
