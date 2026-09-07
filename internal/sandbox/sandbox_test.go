package sandbox

import (
	"context"
	"strings"
	"testing"
)

type fake struct{}

func (fake) Name() string                                 { return "fake" }
func (fake) Prepare(context.Context) error                { return nil }
func (fake) Start(context.Context, Spec) (Sandbox, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	r := Registry{"fake": func(Options) (Provider, error) { return fake{}, nil }}
	if p, err := r.New("fake", Options{}); err != nil || p.Name() != "fake" {
		t.Fatal(p, err)
	}
	if _, err := r.New("nope", Options{}); err == nil || !strings.Contains(err.Error(), "known: fake") {
		t.Fatal(err)
	}
}
