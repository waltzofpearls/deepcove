package session

import "testing"

func TestAgents_EveryAgentCanLogInInsideItsSession(t *testing.T) {
	for _, agent := range allAgents() {
		if len(agent.LoginModes) == 0 || agent.LoginModes[0] != LoginInSession {
			t.Errorf("%s: login modes %v, want logging in inside the session first, the mode every vendor allows",
				agent.Name, agent.LoginModes)
		}
	}
}
