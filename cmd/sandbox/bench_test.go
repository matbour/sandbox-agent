package main

import (
	"testing"
	"time"
)

func TestParseSizes(t *testing.T) {
	got, err := parseSizes("2:2g, 1:512m")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0] != (size{2, "2g"}) || got[1] != (size{1, "512m"}) {
		t.Fatalf("got %v", got)
	}

	for _, bad := range []string{"2", "0:1g", "x:1g", "2:", ""} {
		if _, err := parseSizes(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestPct(t *testing.T) {
	var xs []sample
	for i := 1; i <= 10; i++ {
		xs = append(xs, sample{Start: time.Duration(11-i) * time.Millisecond})
	}

	start := func(x sample) time.Duration { return x.Start }

	for p, want := range map[int]time.Duration{50: 5 * time.Millisecond, 90: 9 * time.Millisecond, 100: 10 * time.Millisecond} {
		if got := pct(xs, start, p); got != want {
			t.Errorf("p%d = %s, want %s", p, got, want)
		}
	}

	if pct(nil, start, 50) != 0 {
		t.Error("empty: not 0")
	}
}
