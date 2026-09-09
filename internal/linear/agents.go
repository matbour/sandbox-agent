package linear

import "context"

// AgentActivity posts an activity in an agent session: kind is thought, response, error or
// elicitation. See https://linear.app/developers/agent-interaction.
func (c *Client) AgentActivity(ctx context.Context, session, kind, body string) error {
	_, err := CreateAgentActivity(ctx, c.client(), session, map[string]any{"type": kind, "body": body})
	return err
}

// AgentSessionLink adds an external link to the session (shown in Linear), e.g. the pull request.
func (c *Client) AgentSessionLink(ctx context.Context, session, label, url string) error {
	_, err := AddAgentSessionLink(ctx, c.client(), session, label, url)
	return err
}
