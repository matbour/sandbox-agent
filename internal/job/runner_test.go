package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matbour/sandbox-agent/internal/config"
)

func testRunner(t *testing.T) *Runner {
	c := &config.Config{TriggerLabel: "agent", AgentModel: "default", StateDir: t.TempDir(),
		ModelLabels: []config.ModelLabel{{Label: "agent-haiku", Model: "haiku"}, {Label: "agent-opus", Model: "opus"}}}
	return &Runner{Cfg: c}
}

func TestRequestModel(t *testing.T) {
	r := testRunner(t)
	for _, tc := range []struct{ request, fallback, want string }{
		{"please fix it", "", "default"},
		{"please fix it", "haiku", "haiku"},                 // keeps the original job's model
		{"the agent should fix it", "haiku", "haiku"},       // "agent" alone is not a model label
		{"redo with `agent-opus`, thanks", "haiku", "opus"}, // an explicit label wins
		{"agent-haiku or agent-opus?", "", "opus"},          // the strongest wins
	} {
		if got := r.requestModel(tc.request, tc.fallback); got != tc.want {
			t.Errorf("%q / %q: got %s, want %s", tc.request, tc.fallback, got, tc.want)
		}
	}
}

func TestWorkdir(t *testing.T) {
	r := testRunner(t)
	w, err := r.newWorkdir("ENG-750", "update")
	if err != nil {
		t.Fatal(err)
	}
	if w.slug != "eng-750" || !strings.HasPrefix(filepath.Base(w.dir), "eng-750-update-") {
		t.Fatalf("names: %+v", w)
	}
	os.WriteFile(filepath.Join(w.dir, "agent.json"), []byte("{}"), 0o600)
	os.MkdirAll(filepath.Join(w.repo, ".git"), 0o700)
	cleanWorkdir(w.dir)
	if _, err := os.Stat(w.ws); !os.IsNotExist(err) {
		t.Fatal("workspace not removed")
	}
	if _, err := os.Stat(filepath.Join(w.dir, "agent.json")); err != nil {
		t.Fatal("logs removed")
	}
}
