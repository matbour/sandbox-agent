package vsock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Listener accepts vsock connections on one port.
type Listener struct {
	f *os.File
}

// Listen listens on port for connections from any context ID (in a guest: from the host).
func Listen(port uint32) (*Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}

	err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port})
	if err == nil {
		err = unix.Listen(fd, 16)
	}

	if err != nil {
		_ = unix.Close(fd)

		return nil, fmt.Errorf("vsock listen %d: %w", port, err)
	}

	return &Listener{os.NewFile(uintptr(fd), fmt.Sprintf("vsock-listen:%d", port))}, nil
}

// Accept waits for the next connection. The descriptor is non-blocking, so waiting goes through
// the runtime's poller and Close unblocks it.
func (l *Listener) Accept() (*Conn, error) {
	rc, err := l.f.SyscallConn()
	if err != nil {
		return nil, err
	}

	var (
		nfd  int
		aerr error
	)

	err = rc.Read(func(fd uintptr) bool {
		nfd, _, aerr = unix.Accept4(int(fd), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)

		return !errors.Is(aerr, unix.EAGAIN)
	})
	if err != nil {
		return nil, err
	}

	if aerr != nil {
		return nil, aerr
	}

	return &Conn{os.NewFile(uintptr(nfd), "vsock")}, nil
}

// Close stops listening.
func (l *Listener) Close() error { return l.f.Close() }
