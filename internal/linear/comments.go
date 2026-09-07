package linear

import (
	"context"
	"time"
)

// Comment is a comment on an issue, with the issue it belongs to.
type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	User      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
	Issue *Issue `json:"issue"`
}

// Comment posts a Markdown comment on the issue.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	q := `mutation($id: String!, $body: String!) {
  commentCreate(input: { issueId: $id, body: $body }) { success } }`
	return c.do(ctx, q, map[string]any{"id": issueID, "body": body}, nil)
}

// CommentsSince returns comments created after since whose body contains text (case-insensitive).
func (c *Client) CommentsSince(ctx context.Context, since time.Time, text string) ([]Comment, error) {
	var out struct {
		Comments struct {
			Nodes []Comment `json:"nodes"`
		} `json:"comments"`
	}
	q := `query($since: DateTimeOrDuration!, $text: String!) {
  comments(first: 50, orderBy: createdAt, filter: { createdAt: { gt: $since }, body: { containsIgnoreCase: $text } }) {
    nodes { id body createdAt user { id name } issue { ` + issueFields + ` } } } }`
	err := c.do(ctx, q, map[string]any{"since": since.UTC().Format(time.RFC3339), "text": text}, &out)
	return out.Comments.Nodes, err
}

// Reply posts a comment in the thread of parentID.
func (c *Client) Reply(ctx context.Context, issueID, parentID, body string) error {
	q := `mutation($i: CommentCreateInput!) { commentCreate(input: $i) { success } }`
	return c.do(ctx, q, map[string]any{"i": map[string]any{"issueId": issueID, "parentId": parentID, "body": body}}, nil)
}
