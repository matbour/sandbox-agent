package main

import (
	"fmt"
	"io"
	"strings"
)

// report prints the outcome for a person.
func report(w io.Writer, r Result, dir string) {
	var b strings.Builder

	fmt.Fprintf(&b, "status: %s  (%s, %.1f s, %d turns, $%.2f)\n", r.Status, r.Provider, r.Seconds, r.Turns, r.CostUSD)

	switch {
	case r.Title != "":
		fmt.Fprintf(&b, "title: %s\n", r.Title)
	case r.Reason != "":
		fmt.Fprintf(&b, "reason: %s\n", r.Reason)
	}

	if r.Question != "" {
		fmt.Fprintf(&b, "question: %s\n", r.Question)
	}

	if r.Patch != "" {
		fmt.Fprintf(&b, "patch: %s (%d files)\n", r.Patch, r.Files)
	} else {
		fmt.Fprintln(&b, "patch: none, nothing changed")
	}

	fmt.Fprintf(&b, "output: %s\n", dir)

	_, _ = io.WriteString(w, b.String())
}
