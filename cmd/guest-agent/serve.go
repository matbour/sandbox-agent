package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/matbour/sandbox-agent/internal/guestexec"
)

// pipeGrace is how long output is still read after a command exits, for children that hold its
// stdout (a dev server started in the background, for example). Then the agent answers anyway.
const pipeGrace = 2 * time.Second

// serve answers one request on c and closes it.
func serve(c io.ReadWriteCloser, r *reaper) {
	defer func() { _ = c.Close() }()

	br := guestexec.NewRequestReader(c)

	req, err := guestexec.ReadRequest(br)
	if err != nil {
		_ = guestexec.WriteEnd(c, guestexec.Result{Error: "bad request: " + err.Error()})

		return
	}

	w := &lockedWriter{w: c}

	switch req.Op {
	case guestexec.OpPing:
		_ = guestexec.WriteEnd(w, guestexec.Result{})
	case guestexec.OpExec:
		_ = guestexec.WriteEnd(w, run(req, br, w, r))
	case guestexec.OpTar:
		res := guestexec.Result{}
		if err := guestexec.WriteTree(guestexec.FrameWriter{W: w, Kind: guestexec.Data}, workspace,
			guestexec.TreeOptions{Exclude: req.Exclude}); err != nil {
			res.Error = err.Error()
		}

		_ = guestexec.WriteEnd(w, res)
	default:
		_ = guestexec.WriteEnd(w, guestexec.Result{Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

// run starts the command in its own process group and streams its output. If the host closes
// the stream before the command ends, the whole group is killed.
func run(req guestexec.Request, in io.Reader, w io.Writer, r *reaper) guestexec.Result {
	if len(req.Argv) == 0 {
		return guestexec.Result{Error: "empty argv"}
	}

	argv0 := req.Argv[0]
	if !strings.Contains(argv0, "/") {
		p, err := exec.LookPath(argv0)
		if err != nil {
			return guestexec.Result{ExitCode: 127, Error: err.Error()}
		}

		argv0 = p
	}

	dir := req.Dir
	if dir == "" {
		dir = workspace
	}

	env := []string{"PATH=" + basePath, "LANG=C.UTF-8", "TERM=dumb"}
	cred := &syscall.Credential{Uid: agentUID, Gid: agentGID, Groups: []uint32{}}

	if req.Root {
		env = append(env, "HOME=/root", "USER=root")
		cred = nil
	} else {
		env = append(env, "HOME="+agentHome, "USER=agent")
	}

	env = append(env, req.Env...)

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return guestexec.Result{Error: err.Error()}
	}
	defer func() { _ = devnull.Close() }()

	outR, outW, err := os.Pipe()
	if err != nil {
		return guestexec.Result{Error: err.Error()}
	}

	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()

		return guestexec.Result{Error: err.Error()}
	}

	p, err := r.start(argv0, req.Argv, &os.ProcAttr{
		Dir: dir, Env: env, Files: []*os.File{devnull, outW, errW},
		Sys: &syscall.SysProcAttr{Setpgid: true, Credential: cred},
	})

	_ = outW.Close()
	_ = errW.Close()

	if err != nil {
		_ = outR.Close()
		_ = errR.Close()

		return guestexec.Result{ExitCode: 127, Error: err.Error()}
	}

	// The host cancels by closing the stream: reading it then fails.
	var finished atomic.Bool

	go func() {
		_, _ = io.Copy(io.Discard, in)

		if !finished.Load() {
			_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		}
	}()

	var copies sync.WaitGroup

	for _, s := range []struct {
		r    *os.File
		kind byte
	}{{outR, guestexec.Stdout}, {errR, guestexec.Stderr}} {
		copies.Add(1)

		go func() {
			defer copies.Done()

			_, _ = io.Copy(guestexec.FrameWriter{W: w, Kind: s.kind}, s.r)
		}()
	}

	ws := r.wait(p.Pid)
	finished.Store(true)

	done := make(chan struct{})

	go func() {
		copies.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(pipeGrace):
		log.Printf("%s: output still open after exit, answering without it", req.Argv[0])
	}

	// Closing the read ends stops the copies; a late write fails on the closed pipe.
	_ = outR.Close()
	_ = errR.Close()

	<-done

	switch {
	case ws.Exited():
		return guestexec.Result{ExitCode: ws.ExitStatus()}
	case ws.Signaled():
		return guestexec.Result{ExitCode: -1, Error: "killed by " + ws.Signal().String()}
	default:
		return guestexec.Result{ExitCode: -1, Error: "ended abnormally"}
	}
}

// lockedWriter serializes frames written by several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.w.Write(p)
}
