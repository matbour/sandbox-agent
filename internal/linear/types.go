package linear

import "time"

// The domain types the rest of agentd uses. Each mirrors one fragment of queries.graphql and is
// built by exactly one function below, so the GraphQL shapes (connections, nodes, pointers to
// nullable scalars) stay inside this package.

// Person is a Linear user: a human, or an application acting as one.
type Person struct {
	ID   string
	Name string
}

// Label is an issue label.
type Label struct {
	ID   string
	Name string
}

// Comment is a comment on an issue.
type Comment struct {
	ID        string
	Body      string
	CreatedAt time.Time
	Author    *Person // nil if no user is recorded or the account was deleted
	Issue     *Issue  // set by CommentsSince; nil for an issue's own Comments
}

// AuthorName returns the author's name, or "someone" when unknown.
func (c Comment) AuthorName() string {
	if c.Author == nil {
		return "someone"
	}

	return c.Author.Name
}

// Issue is the subset of a Linear issue the worker uses.
type Issue struct {
	ID          string
	Identifier  string // ENG-750
	Title       string
	Description string // "" when none
	URL         string
	TeamKey     string
	Creator     *Person // nil if deleted, or created by an integration
	Assignee    *Person
	Delegate    *Person // the agent the issue is delegated to
	Labels      []Label
	Comments    []Comment // the first 50
}

func personFrom(f *PersonFields) *Person {
	return &Person{ID: f.Id, Name: f.Name}
}

func labelFrom(f *LabelFields) Label {
	return Label{ID: f.Id, Name: f.Name}
}

func commentFrom(f *CommentFields) Comment {
	c := Comment{ID: f.Id, Body: f.Body, CreatedAt: f.CreatedAt}
	if f.User != nil {
		c.Author = personFrom(&f.User.PersonFields)
	}

	return c
}

func issueFrom(f *IssueFields) Issue {
	is := Issue{ID: f.Id, Identifier: f.Identifier, Title: f.Title, URL: f.Url, TeamKey: f.Team.Key}
	if f.Description != nil {
		is.Description = *f.Description
	}

	if f.Creator != nil {
		is.Creator = personFrom(&f.Creator.PersonFields)
	}

	if f.Assignee != nil {
		is.Assignee = personFrom(&f.Assignee.PersonFields)
	}

	if f.Delegate != nil {
		is.Delegate = personFrom(&f.Delegate.PersonFields)
	}

	for i := range f.Labels.Nodes {
		is.Labels = append(is.Labels, labelFrom(&f.Labels.Nodes[i].LabelFields))
	}

	for i := range f.Comments.Nodes {
		is.Comments = append(is.Comments, commentFrom(&f.Comments.Nodes[i].CommentFields))
	}

	return is
}
