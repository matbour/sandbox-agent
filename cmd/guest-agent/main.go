// Command guest-agent is PID 1 inside a Firecracker microVM (D46). It finishes setting up the
// guest (file systems, a writable /etc, loopback), then serves the host over vsock: run a command
// and stream its output, or send the workspace back as a tar stream (internal/guestexec).
//
// The guest's init is a one-line shell command that mounts /run, mounts the read-only disk that
// holds this binary, and execs it:
//
//	init=/bin/sh -- -c "mount -t tmpfs -o mode=755 tmpfs /run && mkdir /run/agent &&
//	    mount -t ext4 -o ro /dev/vdc /run/agent && exec /run/agent/guest-agent"
//
// The file system type is named: busybox mount detects it through /proc, which is not mounted yet.
//
// Settings come from the kernel command line: sandbox.dns=<ip>[,<ip>] (default 1.1.1.1) and
// sandbox.hostname=<name>.
//
// It holds no secret and trusts nothing it runs: the processes it starts may be hostile, and so
// may anything else in the guest. Its only job is to be the host's hands inside the VM.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/matbour/sandbox-agent/internal/guestexec"
	"github.com/matbour/sandbox-agent/internal/vsock"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("guest-agent: ")

	if os.Getpid() == 1 {
		if err := setup(); err != nil {
			// PID 1 exiting panics the kernel, which stops the VM: the host sees it.
			log.Fatalf("setup: %v", err)
		}
	}

	// exec.LookPath reads this process's PATH.
	_ = os.Setenv("PATH", basePath)

	r := newReaper()
	go r.run()

	l, err := vsock.Listen(guestexec.Port)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(os.Stderr, "guest-agent: listening on vsock port %d\n", guestexec.Port)

	for {
		c, err := l.Accept()
		if err != nil {
			log.Printf("accept: %v", err)

			continue
		}

		go serve(c, r)
	}
}
