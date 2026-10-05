package ipc

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Client is the host-side IPC client that communicates with the stub server
// running inside namespaces over a SOCK_SEQPACKET socket.
type Client struct {
	mu   sync.Mutex
	conn *os.File
}

// NewClient creates a new IPC client from the host-end socket file.
// The Client takes ownership of the file and will close it when Close is called.
func NewClient(conn *os.File) *Client {
	return &Client{conn: conn}
}

// Call sends a request to the server with the given function ID, payload, and
// optional file descriptors (passed via SCM_RIGHTS). It blocks until a response
// is received, validates that the response cookie matches, and returns the
// response payload, any file descriptors returned by the handler via SCM_RIGHTS,
// or an error. The caller takes ownership of any returned file descriptors and
// must close them.
func (c *Client) Call(funcID uint32, payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Generate a random cookie for request/response correlation.
	cookie, err := randomCookie()
	if err != nil {
		return nil, nil, fmt.Errorf("ipc client: generate cookie: %w", err)
	}

	req := &Request{
		Cookie:  cookie,
		FuncID:  funcID,
		Payload: payload,
	}

	data, err := EncodeRequest(req)
	if err != nil {
		return nil, nil, err
	}

	// Build SCM_RIGHTS control message if fds are provided.
	var oob []byte
	if len(fds) > 0 {
		rawFds := make([]int, len(fds))
		for i, f := range fds {
			rawFds[i] = int(f.Fd())
		}
		oob = unix.UnixRights(rawFds...)
	}

	// Send the message with optional ancillary data.
	connFd := int(c.conn.Fd())
	if _, err := unix.SendmsgN(connFd, data, oob, nil, 0); err != nil {
		return nil, nil, fmt.Errorf("ipc client: sendmsg: %w", err)
	}

	// Receive the response, with space for up to 16 FDs in ancillary data.
	respBuf := make([]byte, MaxMessageSize)
	respOOB := make([]byte, unix.CmsgSpace(16*4))
	n, oobn, _, _, err := unix.Recvmsg(connFd, respBuf, respOOB, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("ipc client: recvmsg: %w", err)
	}
	if n == 0 {
		return nil, nil, fmt.Errorf("ipc client: connection closed by server")
	}

	resp, err := DecodeResponse(respBuf[:n])
	if err != nil {
		return nil, nil, err
	}

	// Validate cookie match.
	if resp.Cookie != cookie {
		return nil, nil, fmt.Errorf("ipc client: cookie mismatch: sent %d, got %d", cookie, resp.Cookie)
	}

	// Parse any file descriptors returned in the response ancillary data.
	var returnedFDs []*os.File
	if oobn > 0 {
		scms, err := unix.ParseSocketControlMessage(respOOB[:oobn])
		if err == nil {
			for _, scm := range scms {
				rawFds, err := unix.ParseUnixRights(&scm)
				if err != nil {
					continue
				}
				for _, fd := range rawFds {
					returnedFDs = append(returnedFDs, os.NewFile(uintptr(fd), fmt.Sprintf("ipc-resp-fd-%d", fd)))
				}
			}
		}
	}

	// If server returned an error, propagate it (close any returned FDs first).
	if resp.Error != "" {
		closeFds(returnedFDs)
		return nil, nil, fmt.Errorf("ipc client: remote error: %s", resp.Error)
	}

	return resp.Payload, returnedFDs, nil
}

// Close closes the underlying socket connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// randomCookie generates a cryptographically random uint64 for request correlation.
func randomCookie() (uint64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}
