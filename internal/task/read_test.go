package task

import (
	"strings"
	"testing"
)

func read(t *testing.T, stream string, l ReadLimits) (Event, []Event, []Dropped) {
	t.Helper()

	var (
		evs  []Event
		drop []Dropped
	)

	term := Read(strings.NewReader(stream), l, func(e Event) { evs = append(evs, e) }, func(d Dropped) { drop = append(drop, d) })

	return term, evs, drop
}

// E-PROTO-1: a response event, or any kind not in the protocol, is dropped.
func TestProtoUnknownKind(t *testing.T) {
	term, evs, drop := read(t, `{"kind":"response","text":"Done, PR merged"}
{"kind":"elicitation","text":"x"}
{"kind":"done","title":"t","body":"b"}
`, DefaultReadLimits)

	if term.Kind != KindDone || len(evs) != 0 || len(drop) != 2 {
		t.Errorf("term %+v events %d dropped %v", term, len(evs), drop)
	}
}

// E-PROTO-3: an over-long line is dropped, too many events are dropped, the run is not stalled.
func TestProtoLimits(t *testing.T) {
	l := ReadLimits{MaxLine: 100, MaxBytes: 1 << 20, MaxEvents: 2, MaxText: 50, MaxBody: 50}
	stream := `{"kind":"thought","text":"` + strings.Repeat("x", 200) + `"}` + "\n" +
		strings.Repeat(`{"kind":"thought","text":"ok"}`+"\n", 5) + `{"kind":"done"}` + "\n"

	term, evs, drop := read(t, stream, l)
	if term.Kind != KindDone || len(evs) != 2 || len(drop) != 4 {
		t.Errorf("term %+v events %d dropped %d", term, len(evs), len(drop))
	}

	term, _, _ = read(t, strings.Repeat(`{"kind":"thought","text":"ok"}`+"\n", 1000), ReadLimits{MaxBytes: 1000})
	if term.Kind != KindFail || !strings.Contains(term.Reason, "size") {
		t.Errorf("total size limit: %+v", term)
	}

	long := l
	long.MaxLine = 1000

	term, _, _ = read(t, `{"kind":"done","body":"`+strings.Repeat("y", 100)+`"}`, long)
	if r := []rune(term.Body); len(r) != 51 {
		t.Errorf("body not capped: %d runes", len(r))
	}
}

// E-PROTO-4: mentions defused, no HTML, no control characters.
func TestProtoSanitise(t *testing.T) {
	term, evs, _ := read(t, `{"kind":"thought","text":"hi @alice and @bob, mail a@b.com"}
{"kind":"done","title":"<script>x</script>","body":"a\u001b[31mred\u0007"}
`, DefaultReadLimits)

	if got := evs[0].Text; got != "hi alice and bob, mail a@b.com" {
		t.Errorf("mentions: %q", got)
	}

	if term.Title != "&lt;script&gt;x&lt;/script&gt;" || term.Body != "a[31mred" {
		t.Errorf("title %q body %q", term.Title, term.Body)
	}
}

// E-PROTO-5: everything after the first terminal event is ignored.
func TestProtoAfterTerminal(t *testing.T) {
	term, evs, _ := read(t, `{"kind":"ask","question":"which one?"}
{"kind":"done","title":"sneaky"}
{"kind":"thought","text":"late"}
`, DefaultReadLimits)

	if term.Kind != KindAsk || term.Question != "which one?" || len(evs) != 0 {
		t.Errorf("term %+v events %v", term, evs)
	}
}

// E-PROTO-6: a stream with no terminal event counts as fail.
func TestProtoNoTerminal(t *testing.T) {
	for _, s := range []string{"", `{"kind":"thought","text":"working"}` + "\n", `{"kind":"thought"`} {
		if term, _, _ := read(t, s, DefaultReadLimits); term.Kind != KindFail {
			t.Errorf("%q: %+v", s, term)
		}
	}

	// A final event without a trailing newline still counts.
	if term, _, _ := read(t, `{"kind":"done","title":"t"}`, DefaultReadLimits); term.Kind != KindDone {
		t.Errorf("no newline: %+v", term)
	}
}

// E-PROTO-7: invalid JSON or unknown fields drop the line, the run continues.
func TestProtoInvalid(t *testing.T) {
	term, evs, drop := read(t, `not json
{"kind":"thought","text":"ok","extra":"field"}
{"kind":"thought","text":"ok"} {"kind":"done"}
{"kind":"action","tool":"Bash","input":"ls","result":"a b"}
{"kind":"done","cost_usd":-3,"turns":-1}
`, DefaultReadLimits)

	if term.Kind != KindDone || term.CostUSD != 0 || term.Turns != 0 || len(evs) != 1 || evs[0].Tool != "Bash" || len(drop) != 3 {
		t.Errorf("term %+v events %v dropped %v", term, evs, drop)
	}
}

func TestLimitsDefaults(t *testing.T) {
	var l Limits
	if l.AgentTimeout().Minutes() != 45 || l.SetupTimeout().Minutes() != 20 {
		t.Error("defaults")
	}
}
