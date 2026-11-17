package main

import (
	"fmt"
	"io"
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

// output runs a short command to completion and returns its combined output: the os/exec
// equivalent of CombinedOutput, through start and wait so it never races with the reaper.
func (r *reaper) output(argv0 string, args ...string) ([]byte, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()

		return nil, err
	}

	p, err := r.start(argv0, append([]string{argv0}, args...), &os.ProcAttr{Files: []*os.File{devnull, pw, pw}})

	_ = pw.Close()
	_ = devnull.Close()

	if err != nil {
		_ = pr.Close()

		return nil, err
	}

	out, _ := io.ReadAll(pr)
	_ = pr.Close()

	ws := r.wait(p.Pid)
	if !ws.Exited() || ws.ExitStatus() != 0 {
		return out, fmt.Errorf("%s: wait status %d", argv0, ws)
	}

	return out, nil
}
