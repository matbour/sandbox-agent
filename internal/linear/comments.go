package linear

import (
	"context"
	"time"
)

// Comment posts a Markdown comment on the issue.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	_, err := CreateComment(ctx, c.client(), issueID, body, nil)
	return err
}

// Reply posts a comment in the thread of parentID.
func (c *Client) Reply(ctx context.Context, issueID, parentID, body string) error {
	_, err := CreateComment(ctx, c.client(), issueID, body, &parentID)
	return err
}

// CommentsSince returns comments created after since whose body contains text (case-insensitive).
func (c *Client) CommentsSince(ctx context.Context, since time.Time, text string) ([]Comment, error) {
	r, err := CommentsSince(ctx, c.client(), since.UTC().Format(time.RFC3339), text)
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(r.Comments.Nodes))
	for i := range r.Comments.Nodes {
		n := &r.Comments.Nodes[i]
		cm := commentFrom(&n.CommentFields)
		if n.Issue != nil {
			is := issueFrom(&n.Issue.IssueFields)
			cm.Issue = &is
		}
		out = append(out, cm)
	}
	return out, nil
}
