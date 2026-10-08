// Package vsock dials AF_VSOCK stream sockets, the channel between a microVM guest and its host
// (docs/microvm.md). Go's net package does not know this address family, so the socket is made
// with raw system calls and wrapped in an *os.File, which still uses the runtime's poller.
//
// With Firecracker, a guest connection to (HostCID, port) reaches the host as a connection to the
// Unix socket "<uds_path>_<port>", where uds_path is the vsock device's path in the VM's config.
package vsock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// HostCID is the context ID of the host, seen from a guest.
const HostCID = unix.VMADDR_CID_HOST

// Conn is a connected vsock stream.
type Conn struct {
	*os.File
}

// Dial connects to port on the machine with context ID cid.
func Dial(cid, port uint32) (*Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}

	// Connect in blocking mode, then switch to non-blocking so that os.NewFile hands the
	// descriptor to the runtime's poller.
	err = unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}

	if err != nil {
		_ = unix.Close(fd)

		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, err)
	}

	return &Conn{os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d:%d", cid, port))}, nil
}

// CloseWrite shuts down the sending side, so the peer reads end of file.
func (c *Conn) CloseWrite() error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}

	var serr error

	err = rc.Control(func(fd uintptr) {
		serr = unix.Shutdown(int(fd), unix.SHUT_WR)
	})
	if err != nil {
		return err
	}

	return serr
}
