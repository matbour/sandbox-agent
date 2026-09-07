package linear

import "context"

// AgentActivity posts an activity in an agent session: kind is thought, response, error or
// elicitation. See https://linear.app/developers/agent-interaction.
func (c *Client) AgentActivity(ctx context.Context, session, kind, body string) error {
	q := `mutation($i: AgentActivityCreateInput!) { agentActivityCreate(input: $i) { success } }`
	return c.do(ctx, q, map[string]any{"i": map[string]any{
		"agentSessionId": session, "content": map[string]any{"type": kind, "body": body}}}, nil)
}

// AgentSessionLink sets the session's external link (shown in Linear), e.g. the pull request.
func (c *Client) AgentSessionLink(ctx context.Context, session, label, url string) error {
	q := `mutation($id: String!, $i: AgentSessionUpdateInput!) { agentSessionUpdate(id: $id, input: $i) { success } }`
	return c.do(ctx, q, map[string]any{"id": session, "i": map[string]any{
		"addedExternalUrls": []map[string]string{{"label": label, "url": url}}}}, nil)
}
