package main

import (
	"strings"
	"sync"
	"testing"
)

// TestOutputWithReaper: with the reaper collecting every child, commands run through output
// must still get their own exit status and output. exec.Command in their place fails now and
// then with "waitid: no child processes", the reaper having taken the child first.
func TestOutputWithReaper(t *testing.T) {
	r := testReaper()

	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			out, err := r.output("/bin/sh", "-c", "echo ok")
			if err != nil || strings.TrimSpace(string(out)) != "ok" {
				t.Errorf("run %d: %q, %v", i, out, err)
			}

			if _, err := r.output("/bin/sh", "-c", "echo no >&2; exit 3"); err == nil {
				t.Errorf("run %d: a failing command reported success", i)
			}
		}()
	}

	wg.Wait()
}

// One reaper per process, as in the guest: a second one would collect the first one's children.
var testReaper = sync.OnceValue(func() *reaper {
	r := newReaper()
	go r.run()

	return r
})
