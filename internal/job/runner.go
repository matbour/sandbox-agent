// Package job runs Linear issues through a sandboxed coding agent and publishes the result as
// GitHub pull requests: new ones (labels, delegation), updates (mentions, fix passes), and
// read-only research (mentions without a pull request).
package job

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/matbour/sandbox-agent/internal/config"
	"github.com/matbour/sandbox-agent/internal/github"
	"github.com/matbour/sandbox-agent/internal/linear"
)

// Marker starts every comment the worker posts, so its own comments are not fed back to the agent.
const Marker = "🤖 sandbox-agent"

// Runner holds what every job needs.
type Runner struct {
	Cfg    *config.Config
	Linear *linear.Client
	App    *github.App
	Log    *slog.Logger
}

func (r *Runner) authorEnv() []string {
	if r.Cfg.AuthorEmail == "" {
		return nil
	}
	return []string{
		"GIT_AUTHOR_NAME=" + r.Cfg.AuthorName, "GIT_AUTHOR_EMAIL=" + r.Cfg.AuthorEmail,
		"GIT_COMMITTER_NAME=" + r.Cfg.AuthorName, "GIT_COMMITTER_EMAIL=" + r.Cfg.AuthorEmail,
	}
}

func modelLabelList(cfg *config.Config) string {
	parts := []string{fmt.Sprintf("`%s` (%s)", cfg.TriggerLabel, cfg.AgentModel)}
	for _, m := range cfg.ModelLabels {
		parts = append(parts, fmt.Sprintf("`%s`", m.Label))
	}
	return strings.Join(parts, ", ")
}

// requestModel returns the model named by a model label written in a request ("agent-opus"),
// else fallback, else the default model.
func (r *Runner) requestModel(request, fallback string) string {
	var words []string
	for _, w := range strings.Fields(request) {
		words = append(words, strings.Trim(w, "`.,;:!?()[]"))
	}
	for _, w := range words {
		for _, m := range r.Cfg.ModelLabels {
			if strings.EqualFold(w, m.Label) {
				model, _ := r.Cfg.ModelFor(words)
				return model
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	return r.Cfg.AgentModel
}
