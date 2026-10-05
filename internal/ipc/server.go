package ipc

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Handler is a function that processes an IPC request. It receives the request
// payload and any file descriptors passed via SCM_RIGHTS, and returns a
// response payload, optional response file descriptors (passed back to the
// caller via SCM_RIGHTS), or an error.
type Handler func(payload []byte, fds []*os.File) ([]byte, []*os.File, error)

// Server is the stub-side IPC server that receives requests from the host
// client over a SOCK_SEQPACKET socket and dispatches them to registered handlers.
type Server struct {
	mu       sync.RWMutex
	conn     *os.File
	handlers map[uint32]Handler
}

// NewServer creates a new IPC server from the child-end socket file.
// The Server takes ownership of the file and will close it when Close is called.
func NewServer(conn *os.File) *Server {
	return &Server{
		conn:     conn,
		handlers: make(map[uint32]Handler),
	}
}

// Register registers a handler for the given function ID.
// Must be called before Serve.
func (s *Server) Register(funcID uint32, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[funcID] = handler
}

// Serve enters the request processing loop. It reads messages from the socket,
// dispatches to the appropriate handler, and sends back responses. It returns
// nil when the socket is closed (EOF / n=0), or an error if something goes wrong.
func (s *Server) Serve() error {
	connFd := int(s.conn.Fd())

	for {
		// Receive a message. Allow space for ancillary data (fd passing).
		// Each fd takes 4 bytes in the SCM_RIGHTS message; allow up to 253 fds
		// (the practical kernel limit per message).
		buf := make([]byte, MaxMessageSize)
		oob := make([]byte, unix.CmsgSpace(253*4))

		n, oobn, _, _, err := unix.Recvmsg(connFd, buf, oob, 0)
		if err != nil {
			return fmt.Errorf("ipc server: recvmsg: %w", err)
		}
		if n == 0 {
			// Socket closed — graceful shutdown.
			return nil
		}

		// Parse any received file descriptors from ancillary data.
		var receivedFds []*os.File
		if oobn > 0 {
			scms, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err != nil {
				return fmt.Errorf("ipc server: parse control message: %w", err)
			}
			for _, scm := range scms {
				fds, err := unix.ParseUnixRights(&scm)
				if err != nil {
					continue
				}
				for _, fd := range fds {
					receivedFds = append(receivedFds, os.NewFile(uintptr(fd), fmt.Sprintf("ipc-fd-%d", fd)))
				}
			}
		}

		// Decode the request.
		req, err := DecodeRequest(buf[:n])
		if err != nil {
			closeFds(receivedFds)
			// Cannot send a response without a valid cookie, so we skip.
			continue
		}

		// Dispatch to handler.
		resp, respFDs := s.dispatch(req, receivedFds)

		// Encode response payload.
		respData, err := EncodeResponse(resp)
		if err != nil {
			// This is a serious internal error.
			closeFds(respFDs)
			return fmt.Errorf("ipc server: encode response: %w", err)
		}

		// Build SCM_RIGHTS ancillary data if the handler returned FDs.
		var respOOB []byte
		if len(respFDs) > 0 {
			rawFds := make([]int, len(respFDs))
			for i, f := range respFDs {
				rawFds[i] = int(f.Fd())
			}
			respOOB = unix.UnixRights(rawFds...)
		}

		if _, err := unix.SendmsgN(connFd, respData, respOOB, nil, 0); err != nil {
			closeFds(respFDs)
			return fmt.Errorf("ipc server: sendmsg: %w", err)
		}

		// Close stub's copies after sending — the kernel duplicated them for the receiver.
		closeFds(respFDs)
	}
}

// dispatch finds and calls the appropriate handler for the request.
func (s *Server) dispatch(req *Request, fds []*os.File) (*Response, []*os.File) {
	s.mu.RLock()
	handler, ok := s.handlers[req.FuncID]
	s.mu.RUnlock()

	resp := &Response{Cookie: req.Cookie}

	if !ok {
		closeFds(fds)
		resp.Error = fmt.Sprintf("unknown function ID: %d", req.FuncID)
		return resp, nil
	}

	result, respFDs, err := handler(req.Payload, fds)
	// Ownership of received fds is transferred to the handler — it must close
	// them or pass them to a child. We do NOT close here.

	if err != nil {
		closeFds(respFDs)
		resp.Error = err.Error()
		return resp, nil
	}
	resp.Payload = result
	return resp, respFDs
}

// Close closes the underlying socket connection.
func (s *Server) Close() error {
	return s.conn.Close()
}

// closeFds closes a slice of files, ignoring errors.
func closeFds(fds []*os.File) {
	for _, f := range fds {
		if f != nil {
			f.Close()
		}
	}
}
