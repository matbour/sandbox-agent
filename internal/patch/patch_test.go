package patch

import (
	"strings"
	"testing"
)

func TestParseRaw(t *testing.T) {
	raw := ":100644 100644 aaaa bbbb M\x00src/a.ts\x00:000000 120000 0000 cccc A\x00link\x00"

	got, err := ParseRaw(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].Path != "src/a.ts" || got[0].Status != 'M' ||
		got[1].NewMode != "120000" || got[1].Status != 'A' {
		t.Fatalf("got %+v", got)
	}

	if _, err := ParseRaw("garbage\x00"); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate(t *testing.T) {
	ok := Change{Status: 'M', Path: "src/app.ts", OldMode: "100644", NewMode: "100644"}

	cases := []struct {
		name   string
		c      Change
		reject string
		warn   string
	}{
		{"plain change", ok, "", ""},
		{"workflow", Change{Status: 'A', Path: ".github/workflows/x.yml", NewMode: "100644"}, "protected path", ""},
		{"nested workflow dir", Change{Status: 'A', Path: "apps/web/.husky/pre-commit", NewMode: "100644"}, "protected path", ""},
		{"delete workflow", Change{Status: 'D', Path: ".github/workflows/ci.yml", OldMode: "100644", NewMode: "000000"}, "protected path", ""},
		{"symlink", Change{Status: 'A', Path: "src/link", NewMode: "120000"}, "symlink", ""},
		{"gitlink", Change{Status: 'A', Path: "vendor/x", NewMode: "160000"}, "submodule", ""},
		{"dotdot", Change{Status: 'A', Path: "a/../b", NewMode: "100644"}, "non-canonical", ""},
		{"git dir", Change{Status: 'A', Path: "sub/.GIT/config", NewMode: "100644"}, "forbidden", ""},
		{"env", Change{Status: 'A', Path: "apps/api/.env.local", NewMode: "100644"}, "environment", ""},
		{"env example", Change{Status: 'A', Path: ".env.example", NewMode: "100644"}, "", ""},
		{"npmrc", Change{Status: 'M', Path: ".npmrc", OldMode: "100644", NewMode: "100644"}, "protected file", ""},
		{"lockfile", Change{Status: 'M', Path: "bun.lock", OldMode: "100644", NewMode: "100644"}, "", "lockfile"},
		{"package.json", Change{Status: 'M', Path: "package.json", OldMode: "100644", NewMode: "100644"}, "", "scripts"},
		{"exec", Change{Status: 'A', Path: "scripts/x.sh", NewMode: "100755"}, "", "executable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Validate([]Change{tc.c}, 100, Limits{MaxFiles: 10, MaxBytes: 1000})
			check(t, "rejection", r.Rejections, tc.reject)
			check(t, "warning", r.Warnings, tc.warn)
		})
	}
}

func TestLimits(t *testing.T) {
	c := Change{Status: 'M', Path: "a", OldMode: "100644", NewMode: "100644"}

	if r := Validate(nil, 0, Limits{}); r.OK() {
		t.Fatal("empty change set accepted")
	}

	if r := Validate([]Change{c, c, c}, 10, Limits{MaxFiles: 2}); r.OK() {
		t.Fatal("too many files accepted")
	}

	if r := Validate([]Change{c}, 5000, Limits{MaxBytes: 1000}); r.OK() {
		t.Fatal("oversized patch accepted")
	}
}

func check(t *testing.T, kind string, got []string, want string) {
	t.Helper()

	if want == "" {
		if len(got) != 0 {
			t.Fatalf("unexpected %s: %v", kind, got)
		}

		return
	}

	if len(got) == 0 || !strings.Contains(strings.Join(got, "|"), want) {
		t.Fatalf("expected %s containing %q, got %v", kind, want, got)
	}
}

func TestDenyPaths(t *testing.T) {
	l := Limits{DenyPaths: []string{"contracts/", "bun.lock"}}

	for p, want := range map[string]bool{
		"contracts/token-v1/src/A.sol": false, "bun.lock": false, "apps/bun.lock": true,
		"contractsx/a.ts": true, "apps/web/a.ts": true,
	} {
		c := Change{Status: 'M', Path: p, OldMode: "100644", NewMode: "100644"}
		if got := Validate([]Change{c}, 10, l).OK(); got != want {
			t.Errorf("%s: OK=%v, want %v", p, got, want)
		}
	}
}
