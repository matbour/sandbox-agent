package config

import (
	"fmt"
	"strings"
)

// ModelLabel maps a Linear label to the model it selects.
type ModelLabel struct {
	Label string
	Model string
}

// DefaultModelLabels lists the explicit model labels, from the lightest to the strongest.
const DefaultModelLabels = "agent-haiku=claude-haiku-5-5,agent-sonnet=claude-sonnet-5-5," +
	"agent-opus=claude-opus-5-5,agent-fable=claude-fable-5-2"

func parseModelLabels(s string) ([]ModelLabel, error) {
	var out []ModelLabel
	for _, item := range list(s) {
		label, model, ok := strings.Cut(item, "=")
		label, model = strings.TrimSpace(label), strings.TrimSpace(model)
		if !ok || label == "" || model == "" {
			return nil, fmt.Errorf("expected label=model, got %q", item)
		}
		out = append(out, ModelLabel{Label: label, Model: model})
	}
	return out, nil
}

// Labels returns every label that triggers a job: the default one, then the model labels.
func (c *Config) Labels() []string {
	labels := []string{c.TriggerLabel}
	for _, m := range c.ModelLabels {
		labels = append(labels, m.Label)
	}
	return labels
}

// ModelFor returns the model selected by an issue's labels: the strongest explicit model label,
// else AgentModel. ok is false if no trigger label is present.
func (c *Config) ModelFor(labels []string) (model string, ok bool) {
	best := -1
	for _, l := range labels {
		if strings.EqualFold(l, c.TriggerLabel) {
			ok = true
		}
		for i, m := range c.ModelLabels {
			if strings.EqualFold(l, m.Label) && i > best {
				best, ok = i, true
			}
		}
	}
	if best >= 0 {
		return c.ModelLabels[best].Model, true
	}
	return c.AgentModel, ok
}

// IsTrigger reports whether label is one of the labels that start a job.
func (c *Config) IsTrigger(label string) bool {
	for _, l := range c.Labels() {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}
