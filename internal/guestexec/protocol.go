// Package guestexec is the control channel between a host and the guest agent inside a microVM
// (cmd/guest-agent, D46). It carries commands to run and their output, and the workspace back to
// the host, over one stream per request (a vsock connection with Firecracker).
//
// A request is one line of JSON from the host. The guest answers with frames, each a kind byte,
// a big-endian uint32 length and that many bytes:
//
//	'o' stdout bytes   'e' stderr bytes   'd' tar bytes (the workspace)
//	'x' the end: JSON Result, always the last frame
//
// When the request sets Stdin, the host then sends the command's input the same way: 'i' frames,
// and one empty 'c' frame for end of input.
//
// The host cancels a request by closing the stream; the guest then kills what it started.
//
// The guest is hostile: everything it sends is bounded (MaxFrame) and the workspace it returns
// goes through Mirror, which refuses paths outside the target directory.
package guestexec

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Port is the vsock port the guest agent listens on.
const Port = 1024

// MaxFrame bounds one frame, so a hostile guest cannot make the host allocate without limit.
const MaxFrame = 1 << 20

// maxRequest bounds the request line read by the guest.
const maxRequest = 1 << 20

// Frame kinds.
const (
	Stdout byte = 'o'
	Stderr byte = 'e'
	Data   byte = 'd'
	End    byte = 'x'

	StdinData  byte = 'i' // host to guest
	StdinClose byte = 'c' // host to guest: end of input
)

// Operations.
const (
	OpPing      = "ping"      // answers End at once: the agent is up
	OpExec      = "exec"      // runs Argv
	OpTar       = "tar"       // sends Dir as a tar stream
	OpConfigure = "configure" // makes a guest restored from a snapshot its own (Configure)
)

// Configure is what a guest restored from a template snapshot (D49) must not keep from the
// template: its name, its clock, its randomness, and its workspace, which the template never
// mounted. The guest applies it once, right after the restore.
type Configure struct {
	Hostname string `json:"hostname,omitempty"`
	// UnixNano sets the guest's clock. KVM's clock_realtime does it on x86 only.
	UnixNano int64 `json:"unix_nano,omitempty"`
	// Entropy is mixed into the guest's random pool, which then reseeds: every guest restored
	// from one snapshot starts with the same pool.
	Entropy []byte `json:"entropy,omitempty"`
	// Workspace mounts /dev/vdb on the workspace, after dropping the block cache the template
	// kept of the disk it was built with.
	Workspace bool `json:"workspace,omitempty"`
	// Net replaces the template's address on eth0. Every guest restored from one template has
	// its MAC address too, so it is set here as well.
	Net *Net `json:"net,omitempty"`
}

// Net is the guest's address on eth0.
type Net struct {
	Address string `json:"address"` // CIDR, e.g. 10.230.0.12/24
	Gateway string `json:"gateway"`
	MAC     string `json:"mac"`
}

// Request is what the host asks for.
type Request struct {
	Op string `json:"op"`

	// exec
	Argv []string `json:"argv,omitempty"`
	Env  []string `json:"env,omitempty"` // KEY=VALUE, added to the agent's base environment
	Dir  string   `json:"dir,omitempty"` // empty: the workspace
	Root bool     `json:"root,omitempty"`
	// Stdin: the host sends input frames after the request; otherwise stdin is /dev/null.
	Stdin bool `json:"stdin,omitempty"`

	// tar
	Exclude []string `json:"exclude,omitempty"` // patterns, see Excluded

	// configure
	Configure *Configure `json:"configure,omitempty"`
}

// Result ends every answer.
type Result struct {
	ExitCode int    `json:"exit_code"`       // exec: the command's exit code, -1 if killed
	Error    string `json:"error,omitempty"` // the agent could not do what was asked
}

// WriteRequest sends r as one line.
func WriteRequest(w io.Writer, r Request) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}

	_, err = w.Write(append(b, '\n'))

	return err
}

// ReadRequest reads the request line.
func ReadRequest(r *bufio.Reader) (Request, error) {
	var req Request

	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return req, errors.New("request too long")
	}

	if err != nil {
		return req, err
	}

	if len(line) > maxRequest {
		return req, errors.New("request too long")
	}

	err = json.Unmarshal(line, &req)

	return req, err
}

// NewRequestReader returns a reader sized for ReadRequest.
func NewRequestReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, maxRequest+1) }

// WriteFrame sends one frame. Payloads over MaxFrame are split.
func WriteFrame(w io.Writer, kind byte, p []byte) error {
	for {
		n := min(len(p), MaxFrame)

		var hdr [5]byte

		hdr[0] = kind
		binary.BigEndian.PutUint32(hdr[1:], uint32(n))

		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}

		if _, err := w.Write(p[:n]); err != nil {
			return err
		}

		p = p[n:]
		if len(p) == 0 {
			return nil
		}
	}
}

// WriteEnd sends the closing frame.
func WriteEnd(w io.Writer, res Result) error {
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}

	return WriteFrame(w, End, b)
}

// FrameWriter turns writes into frames of one kind.
type FrameWriter struct {
	W    io.Writer
	Kind byte
}

func (f FrameWriter) Write(p []byte) (int, error) {
	if err := WriteFrame(f.W, f.Kind, p); err != nil {
		return 0, err
	}

	return len(p), nil
}

// ReadFrame reads one frame into buf (at least MaxFrame bytes) and returns its kind and payload.
func ReadFrame(r io.Reader, buf []byte) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}

	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("frame of %d bytes over the limit", n)
	}

	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return 0, nil, fmt.Errorf("short frame: %w", err)
	}

	return hdr[0], buf[:n], nil
}

// ReadFrames reads frames until End and hands each payload to the writer of its kind. A kind
// with no writer is an error: the guest sent something that was not asked for.
func ReadFrames(r io.Reader, sinks map[byte]io.Writer) (Result, error) {
	var (
		res Result
		hdr [5]byte
		buf = make([]byte, MaxFrame)
	)

	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return res, fmt.Errorf("guest closed the stream without an end frame: %w", err)
		}

		n := binary.BigEndian.Uint32(hdr[1:])
		if n > MaxFrame {
			return res, fmt.Errorf("frame of %d bytes over the limit", n)
		}

		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return res, fmt.Errorf("short frame: %w", err)
		}

		if hdr[0] == End {
			if err := json.Unmarshal(buf[:n], &res); err != nil {
				return res, fmt.Errorf("end frame: %w", err)
			}

			return res, nil
		}

		w, ok := sinks[hdr[0]]
		if !ok || w == nil {
			return res, fmt.Errorf("unexpected frame kind %q", hdr[0])
		}

		if _, err := w.Write(buf[:n]); err != nil {
			return res, err
		}
	}
}
