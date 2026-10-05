// Package ipc implements the communication protocol between the host-side
// ExecEnv layers and the stub binary running inside namespaces. It uses
// SOCK_SEQPACKET Unix sockets with SCM_RIGHTS for file descriptor passing.
package ipc

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
	"syscall"
)

// MaxMessageSize is the maximum size of a single IPC message (64KB).
const MaxMessageSize = 64 * 1024

// Request is a message sent from the host (client) to the stub (server).
type Request struct {
	Cookie  uint64
	FuncID  uint32
	Payload []byte
}

// Response is a message sent from the stub (server) back to the host (client).
type Response struct {
	Cookie  uint64
	Error   string
	Payload []byte
}

// EncodeRequest serializes a Request into bytes using gob encoding.
func EncodeRequest(req *Request) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(req); err != nil {
		return nil, fmt.Errorf("ipc: encode request: %w", err)
	}
	if buf.Len() > MaxMessageSize {
		return nil, fmt.Errorf("ipc: encoded request exceeds max message size (%d > %d)", buf.Len(), MaxMessageSize)
	}
	return buf.Bytes(), nil
}

// DecodeRequest deserializes bytes into a Request.
func DecodeRequest(data []byte) (*Request, error) {
	var req Request
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&req); err != nil {
		return nil, fmt.Errorf("ipc: decode request: %w", err)
	}
	return &req, nil
}

// EncodeResponse serializes a Response into bytes using gob encoding.
func EncodeResponse(resp *Response) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(resp); err != nil {
		return nil, fmt.Errorf("ipc: encode response: %w", err)
	}
	if buf.Len() > MaxMessageSize {
		return nil, fmt.Errorf("ipc: encoded response exceeds max message size (%d > %d)", buf.Len(), MaxMessageSize)
	}
	return buf.Bytes(), nil
}

// DecodeResponse deserializes bytes into a Response.
func DecodeResponse(data []byte) (*Response, error) {
	var resp Response
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ipc: decode response: %w", err)
	}
	return &resp, nil
}

// NewSocketPair creates a connected pair of SOCK_SEQPACKET Unix sockets.
// The first returned file is the host end (for the Client), and the second
// is the child end (for the Server / stub binary).
func NewSocketPair() (host *os.File, child *os.File, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("ipc: socketpair: %w", err)
	}
	host = os.NewFile(uintptr(fds[0]), "ipc-host")
	child = os.NewFile(uintptr(fds[1]), "ipc-child")
	return host, child, nil
}
