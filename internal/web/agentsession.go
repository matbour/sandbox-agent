package web

import (
	"encoding/json"
	"errors"
)

// AgentSessionEvent is the part of a Linear AgentSessionEvent webhook the worker uses.
type AgentSessionEvent struct {
	Action      string // created or prompted
	SessionID   string
	IssueID     string
	Issue       string // identifier
	CommentID   string
	Request     string // the mention comment, or the follow-up message
	Requester   string
	RequesterID string
	Signal      string // "stop" when the person pressed Stop in the session
	Raw         []byte
}

func parseAgentSession(body []byte) (AgentSessionEvent, error) {
	var p struct {
		Action       string `json:"action"`
		AgentSession struct {
			ID    string `json:"id"`
			Issue *struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
			} `json:"issue"`
			Comment *struct {
				ID   string `json:"id"`
				Body string `json:"body"`
			} `json:"comment"`
			Creator *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"creator"`
		} `json:"agentSession"`
		AgentActivity *struct {
			Signal  string `json:"signal"`
			Body    string `json:"body"`
			Content struct {
				Body string `json:"body"`
			} `json:"content"`
			User *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"user"`
		} `json:"agentActivity"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return AgentSessionEvent{}, err
	}
	if p.AgentSession.ID == "" || p.AgentSession.Issue == nil {
		return AgentSessionEvent{}, errors.New("no session or issue")
	}
	ev := AgentSessionEvent{Action: p.Action, SessionID: p.AgentSession.ID, IssueID: p.AgentSession.Issue.ID,
		Issue: p.AgentSession.Issue.Identifier, Raw: body}
	if c := p.AgentSession.Comment; c != nil {
		ev.CommentID, ev.Request = c.ID, c.Body
	}
	if cr := p.AgentSession.Creator; cr != nil {
		ev.Requester, ev.RequesterID = cr.Name, cr.ID
	}
	if a := p.AgentActivity; p.Action == "prompted" && a != nil {
		ev.Signal = a.Signal
		ev.Request = a.Body
		if ev.Request == "" {
			ev.Request = a.Content.Body
		}
		if a.User != nil {
			ev.Requester, ev.RequesterID = a.User.Name, a.User.ID
		}
	}
	return ev, nil
}
