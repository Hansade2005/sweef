package main

import (
	"github.com/sweefbrowser/sweef/internal/agentbridge"
	"github.com/sweefbrowser/sweef/internal/app"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// The sidebar agent: internal/agent behind app.AgentHost. The API key comes
// from the sidebar/settings (agent.setKey, stored in the profile directory:
// DPAPI-protected on Windows, a 0600 file elsewhere) or, for development, from
// ANTHROPIC_API_KEY. SWEEF_AGENT_BASE_URL points the agent at another origin
// (a proxy). Nothing about the key is ever logged or sent to the chrome page.
func init() {
	guiAgent = func(headless bool) app.AgentHost {
		o := agentbridge.Options{}
		if !headless {
			// private windows have no profile directory of their own; the key is
			// not browsing data, so it still lives with the user's profile
			o.KeyDir = store.DefaultDir()
		}
		return agentbridge.New(o)
	}
}
