package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpawn(t *testing.T) {
	d := &daemon{wake: make(chan struct{}, 1), slots: make(chan struct{}, 2), inFlight: map[string]bool{}}
	ctx := context.Background()

	var running, peak, ran atomic.Int32

	release := make(chan struct{})
	job := func() {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}

		<-release
		running.Add(-1)
		ran.Add(1)
	}

	d.spawn(ctx, "ENG-1", job)
	d.spawn(ctx, "ENG-1", job) // in flight: skipped
	d.spawn(ctx, "ENG-2", job)

	started := make(chan struct{})

	go func() {
		d.spawn(ctx, "ENG-3", job) // blocks until a slot frees up
		close(started)
	}()

	select {
	case <-started:
		t.Fatal("a third job started with two slots")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-started
	d.jobs.Wait()

	if ran.Load() != 3 || peak.Load() > 2 {
		t.Errorf("ran %d jobs (want 3), at most %d at once (want 2)", ran.Load(), peak.Load())
	}
}
