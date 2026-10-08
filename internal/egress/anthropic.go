package egress

import "net/url"

// AnthropicUpstream is the Anthropic API.
const AnthropicUpstream = "https://api.anthropic.com"

// Anthropic returns the route for the Anthropic API under /anthropic. The agent sets
// ANTHROPIC_BASE_URL to http://<forwarder>/anthropic and any non-empty ANTHROPIC_API_KEY.
//
// Only the calls Claude Code needs to work are allowed. Others are rejected and logged, which is
// how a missing one shows up.
func Anthropic(upstream *url.URL, key string) Route {
	return Route{
		Prefix:   "/anthropic",
		Upstream: upstream,
		Header:   "x-api-key",
		Key:      key,
		Allow: []string{
			"POST /v1/messages",
			"POST /v1/messages/count_tokens",
		},
	}
}
