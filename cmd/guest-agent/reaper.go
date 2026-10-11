package main

import (
	"os"
	"os/signal"
	"sync"

	"golang.org/x/sys/unix"
)

// reaper collects every child that exits. As PID 1 the agent also inherits orphans, which must be
// reaped or they stay zombies. It is the only caller of wait4 in the process: commands are
// started with os.StartProcess and their status comes from here, never from Process.Wait, which
// would race with wait4(-1).
type reaper struct {
	mu      sync.Mutex
	waiting map[int]chan unix.WaitStatus
	exited  map[int]unix.WaitStatus // started by us, exited before wait was called
	started map[int]bool
}

func newReaper() *reaper {
	return &reaper{waiting: map[int]chan unix.WaitStatus{}, exited: map[int]unix.WaitStatus{}, started: map[int]bool{}}
}

func (r *reaper) run() {
	sig := make(chan os.Signal, 64)
	signal.Notify(sig, unix.SIGCHLD)

	for range sig {
		r.reap()
	}
}

func (r *reaper) reap() {
	for {
		var ws unix.WaitStatus

		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}

		r.mu.Lock()

		if ch, ok := r.waiting[pid]; ok {
			ch <- ws

			delete(r.waiting, pid)
		} else if r.started[pid] {
			r.exited[pid] = ws
		}

		delete(r.started, pid)
		r.mu.Unlock()
	}
}

// start runs os.StartProcess while holding the lock, so the reaper cannot see the child exit
// before it is registered.
func (r *reaper) start(argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, err := os.StartProcess(argv0, argv, attr)
	if err == nil {
		r.started[p.Pid] = true
	}

	return p, err
}

// wait returns the exit status of a child started with start.
func (r *reaper) wait(pid int) unix.WaitStatus {
	r.mu.Lock()

	if ws, ok := r.exited[pid]; ok {
		delete(r.exited, pid)
		r.mu.Unlock()

		return ws
	}

	ch := make(chan unix.WaitStatus, 1)
	r.waiting[pid] = ch
	r.mu.Unlock()

	return <-ch
}
