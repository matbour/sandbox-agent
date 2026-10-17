package task

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// ReadLimits bound what the manager accepts from one worker (E-PROTO). The worker runs next to an
// agent that may have been prompt-injected, so its output is hostile.
type ReadLimits struct {
	MaxLine   int // bytes per line; longer lines are dropped
	MaxBytes  int // bytes per run; past it, the run fails
	MaxEvents int // non-terminal events per run; past it, they are dropped
	MaxText   int // runes per text field
	MaxBody   int // runes for Body
}

// DefaultReadLimits are the limits of agentd.
var DefaultReadLimits = ReadLimits{MaxLine: 256 << 10, MaxBytes: 32 << 20, MaxEvents: 5000, MaxText: 4000, MaxBody: 20000}

// Dropped describes a line the reader refused, for the run's log.
type Dropped struct {
	Line   int
	Reason string
}

// Read consumes the worker's stdout. emit receives every accepted non-terminal event, already
// sanitised; drop receives what was refused (either may be nil). It returns the terminal event.
// A stream that ends without one, or goes over MaxBytes, yields a fail event (E-PROTO-6); events
// after the first terminal event are ignored (E-PROTO-5).
func Read(r io.Reader, l ReadLimits, emit func(Event), drop func(Dropped)) Event {
	if emit == nil {
		emit = func(Event) {}
	}

	if drop == nil {
		drop = func(Dropped) {}
	}

	br := bufio.NewReaderSize(r, 64<<10)

	var (
		total, events, n int
		line             []byte
		tooLong          bool
	)

	for {
		chunk, err := br.ReadSlice('\n')
		total += len(chunk)

		if l.MaxBytes > 0 && total > l.MaxBytes {
			_, _ = io.Copy(io.Discard, br)

			return Event{Kind: KindFail, Reason: "the worker's output went over the size limit"}
		}

		if !tooLong {
			line = append(line, chunk...)
			if l.MaxLine > 0 && len(line) > l.MaxLine {
				tooLong, line = true, line[:0]
			}
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if len(chunk) > 0 || len(line) > 0 || tooLong {
			n++

			switch {
			case tooLong:
				drop(Dropped{n, "line over the size limit"})
			case len(bytes.TrimSpace(line)) > 0:
				ev, reason := parse(line, l)
				switch {
				case reason != "":
					drop(Dropped{n, reason})
				case ev.Terminal():
					_, _ = io.Copy(io.Discard, br)

					return ev
				case l.MaxEvents > 0 && events >= l.MaxEvents:
					drop(Dropped{n, "over the event limit"})
				default:
					events++

					emit(ev)
				}
			}
		}

		line, tooLong = line[:0], false

		if err != nil {
			return Event{Kind: KindFail, Reason: "the worker ended without a final event"}
		}
	}
}

var knownKinds = map[string]bool{KindThought: true, KindAction: true, KindAsk: true, KindDone: true, KindFail: true}

// parse decodes one line strictly: unknown fields and unknown kinds are refused (E-PROTO-1, 7).
func parse(line []byte, l ReadLimits) (Event, string) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()

	var ev Event
	if err := dec.Decode(&ev); err != nil {
		return ev, "invalid JSON or unknown field"
	}

	if dec.More() {
		return ev, "more than one value on the line"
	}

	if !knownKinds[ev.Kind] {
		return ev, "unknown kind " + strings.TrimSpace(clean(ev.Kind, 40))
	}

	for _, f := range []*string{&ev.Text, &ev.Tool, &ev.Input, &ev.Result, &ev.Question, &ev.Title, &ev.Reason} {
		*f = clean(*f, l.MaxText)
	}

	ev.Body = clean(ev.Body, l.MaxBody)

	if ev.CostUSD < 0 {
		ev.CostUSD = 0
	}

	if ev.Turns < 0 {
		ev.Turns = 0
	}

	return ev, ""
}

// mention matches an @ that would ping someone: at the start or after a non-word character.
var mention = regexp.MustCompile(`(^|[^\w])@(\w)`)

// clean makes worker text safe to post (E-PROTO-4): control characters removed except newlines
// and tabs, @ mentions defused, < and > escaped so nothing renders as HTML, and the length capped.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}

		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}

		return r
	}, s)

	s = mention.ReplaceAllString(s, "$1$2")
	s = strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)

	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max]) + "…"
	}

	return s
}
