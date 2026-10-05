package ipc

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
)

func TestBasicRequestResponse(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	server := NewServer(child)
	server.Register(1, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		return append([]byte("echo:"), payload...), nil, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(); err != nil {
			t.Errorf("server.Serve: %v", err)
		}
	}()

	client := NewClient(host)

	resp, _, err := client.Call(1, []byte("hello"), nil)
	if err != nil {
		t.Fatalf("client.Call: %v", err)
	}
	if string(resp) != "echo:hello" {
		t.Fatalf("unexpected response: %q", resp)
	}

	// Close client to signal server shutdown.
	client.Close()
	wg.Wait()
}

func TestFdPassing(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	// Create a pipe. We'll pass the write end to the server.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	server := NewServer(child)
	server.Register(2, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		if len(fds) != 1 {
			return nil, nil, fmt.Errorf("expected 1 fd, got %d", len(fds))
		}
		// Write the payload to the received fd (the write end of the pipe).
		_, err := fds[0].Write(payload)
		fds[0].Close()
		if err != nil {
			return nil, nil, err
		}
		return []byte("written"), nil, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(); err != nil {
			t.Errorf("server.Serve: %v", err)
		}
	}()

	client := NewClient(host)

	resp, _, err := client.Call(2, []byte("test-data"), []*os.File{pw})
	if err != nil {
		t.Fatalf("client.Call: %v", err)
	}
	if string(resp) != "written" {
		t.Fatalf("unexpected response: %q", resp)
	}

	// Close the write end on our side (server has its own copy now closed).
	pw.Close()

	// Read from the pipe to verify data was written by the server handler.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, pr); err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	if buf.String() != "test-data" {
		t.Fatalf("pipe data mismatch: got %q, want %q", buf.String(), "test-data")
	}

	client.Close()
	wg.Wait()
}

// TestFdPassingInResponse verifies that handlers can return FDs via SCM_RIGHTS
// in the response, and the client receives and can use them.
func TestFdPassingInResponse(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	// Create a pipe. The server will return the write end in its response.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	server := NewServer(child)
	server.Register(10, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		closeFds(fds)
		// Return the write end of the pipe as a response FD.
		return []byte("fd-returned"), []*os.File{pw}, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(); err != nil {
			t.Errorf("server.Serve: %v", err)
		}
	}()

	client := NewClient(host)

	respPayload, respFDs, err := client.Call(10, nil, nil)
	if err != nil {
		t.Fatalf("client.Call: %v", err)
	}
	if string(respPayload) != "fd-returned" {
		t.Fatalf("unexpected response payload: %q", respPayload)
	}
	if len(respFDs) != 1 {
		t.Fatalf("expected 1 response FD, got %d", len(respFDs))
	}
	defer respFDs[0].Close()

	// The server closed its copy of pw after sending; the pipe stays open
	// because the client received a duplicate. Write to the client's copy.
	pw.Close() // close our original (server already closed its copy on send)
	if _, err := respFDs[0].Write([]byte("response-fd-data")); err != nil {
		t.Fatalf("write to response FD: %v", err)
	}
	respFDs[0].Close()

	// Read from the read end to verify the data came through.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, pr); err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	if buf.String() != "response-fd-data" {
		t.Fatalf("pipe data mismatch: got %q, want %q", buf.String(), "response-fd-data")
	}

	client.Close()
	wg.Wait()
}

func TestMultipleSequentialCalls(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	callCount := 0
	server := NewServer(child)
	server.Register(1, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		callCount++
		return []byte(fmt.Sprintf("call-%d", callCount)), nil, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.Serve()
	}()

	client := NewClient(host)

	for i := 1; i <= 5; i++ {
		resp, _, err := client.Call(1, nil, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		expected := fmt.Sprintf("call-%d", i)
		if string(resp) != expected {
			t.Fatalf("call %d: got %q, want %q", i, resp, expected)
		}
	}

	client.Close()
	wg.Wait()
}

func TestErrorPropagation(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	server := NewServer(child)
	server.Register(1, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		return nil, nil, fmt.Errorf("something went wrong")
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.Serve()
	}()

	client := NewClient(host)

	_, _, err = client.Call(1, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "ipc client: remote error: something went wrong" {
		t.Fatalf("unexpected error message: %q", got)
	}

	client.Close()
	wg.Wait()
}

func TestUnknownFuncID(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	server := NewServer(child)
	// Register nothing.

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.Serve()
	}()

	client := NewClient(host)

	_, _, err = client.Call(99, []byte("data"), nil)
	if err == nil {
		t.Fatal("expected error for unknown funcID")
	}
	if got := err.Error(); got != "ipc client: remote error: unknown function ID: 99" {
		t.Fatalf("unexpected error: %q", got)
	}

	client.Close()
	wg.Wait()
}

func TestServerGracefulShutdown(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer child.Close()

	server := NewServer(child)

	done := make(chan error, 1)
	go func() {
		done <- server.Serve()
	}()

	// Close the host end — server should detect EOF and return nil.
	host.Close()

	err = <-done
	if err != nil {
		t.Fatalf("expected nil on graceful shutdown, got: %v", err)
	}
}

func TestEncodeDecodeRequest(t *testing.T) {
	req := &Request{
		Cookie:  12345,
		FuncID:  7,
		Payload: []byte("test payload"),
	}
	data, err := EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	decoded, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if decoded.Cookie != req.Cookie {
		t.Errorf("cookie: got %d, want %d", decoded.Cookie, req.Cookie)
	}
	if decoded.FuncID != req.FuncID {
		t.Errorf("funcID: got %d, want %d", decoded.FuncID, req.FuncID)
	}
	if !bytes.Equal(decoded.Payload, req.Payload) {
		t.Errorf("payload: got %q, want %q", decoded.Payload, req.Payload)
	}
}

func TestEncodeDecodeResponse(t *testing.T) {
	resp := &Response{
		Cookie:  99999,
		Error:   "",
		Payload: []byte("response data"),
	}
	data, err := EncodeResponse(resp)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	decoded, err := DecodeResponse(data)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if decoded.Cookie != resp.Cookie {
		t.Errorf("cookie: got %d, want %d", decoded.Cookie, resp.Cookie)
	}
	if decoded.Error != resp.Error {
		t.Errorf("error: got %q, want %q", decoded.Error, resp.Error)
	}
	if !bytes.Equal(decoded.Payload, resp.Payload) {
		t.Errorf("payload: got %q, want %q", decoded.Payload, resp.Payload)
	}
}

func TestNewSocketPair(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	// Verify they are valid file descriptors by checking Fd().
	if host.Fd() == ^uintptr(0) {
		t.Fatal("host fd is invalid")
	}
	if child.Fd() == ^uintptr(0) {
		t.Fatal("child fd is invalid")
	}
}

func TestMultipleFdPassing(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	// Create two pipes.
	pr1, pw1, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe 1: %v", err)
	}
	defer pr1.Close()

	pr2, pw2, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe 2: %v", err)
	}
	defer pr2.Close()

	server := NewServer(child)
	server.Register(3, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		if len(fds) != 2 {
			return nil, nil, fmt.Errorf("expected 2 fds, got %d", len(fds))
		}
		// Write different data to each fd.
		fds[0].Write([]byte("data1"))
		fds[0].Close()
		fds[1].Write([]byte("data2"))
		fds[1].Close()
		return []byte("ok"), nil, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.Serve()
	}()

	client := NewClient(host)

	resp, _, err := client.Call(3, nil, []*os.File{pw1, pw2})
	if err != nil {
		t.Fatalf("client.Call: %v", err)
	}
	if string(resp) != "ok" {
		t.Fatalf("unexpected response: %q", resp)
	}

	// Close write ends on our side.
	pw1.Close()
	pw2.Close()

	// Verify data on both pipes.
	buf1 := make([]byte, 64)
	n1, _ := pr1.Read(buf1)
	if string(buf1[:n1]) != "data1" {
		t.Fatalf("pipe1: got %q, want %q", buf1[:n1], "data1")
	}

	buf2 := make([]byte, 64)
	n2, _ := pr2.Read(buf2)
	if string(buf2[:n2]) != "data2" {
		t.Fatalf("pipe2: got %q, want %q", buf2[:n2], "data2")
	}

	client.Close()
	wg.Wait()
}

func TestLargePayload(t *testing.T) {
	host, child, err := NewSocketPair()
	if err != nil {
		t.Fatalf("NewSocketPair: %v", err)
	}
	defer host.Close()
	defer child.Close()

	server := NewServer(child)
	server.Register(1, func(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
		// Echo back the payload.
		return payload, nil, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		server.Serve()
	}()

	client := NewClient(host)

	// Create a payload that's large but under the limit.
	// Gob overhead means we can't use exactly MaxMessageSize bytes of payload.
	largePayload := make([]byte, 32*1024) // 32KB
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	resp, _, err := client.Call(1, largePayload, nil)
	if err != nil {
		t.Fatalf("client.Call with large payload: %v", err)
	}
	if !bytes.Equal(resp, largePayload) {
		t.Fatal("large payload round-trip mismatch")
	}

	client.Close()
	wg.Wait()
}

func TestTypedPayloadRoundTrip(t *testing.T) {
	t.Run("Exec", func(t *testing.T) {
		in := ExecPayload{
			Argv: []string{"/bin/sh", "-c", "echo hi"}, Cwd: "/tmp",
			Env: []string{"PATH=/bin", "HOME=/root"}, HasCreds: true,
			UID: 1000, GID: 1000, UnshareFlags: 0x10000000,
		}
		b, err := EncodeExecRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodeExecRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%#v", in) != fmt.Sprintf("%#v", out) {
			t.Errorf("round-trip mismatch:\n in=%#v\nout=%#v", in, out)
		}
	})
	t.Run("Mount", func(t *testing.T) {
		in := MountPayload{Source: "/a", Target: "/b", FSType: "tmpfs", Flags: 42, Data: "size=1g"}
		b, err := EncodeMountRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodeMountRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
	t.Run("Umount", func(t *testing.T) {
		in := UmountPayload{Target: "/a", Flags: 7}
		b, err := EncodeUmountRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodeUmountRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
	t.Run("Symlink", func(t *testing.T) {
		in := SymlinkPayload{Target: "/proc/self/fd/0", Linkpath: "/dev/stdin"}
		b, err := EncodeSymlinkRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodeSymlinkRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
	t.Run("Path", func(t *testing.T) {
		in := PathPayload{Path: "/some/path"}
		b, err := EncodePathRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodePathRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
	t.Run("PathMode", func(t *testing.T) {
		in := PathModePayload{Path: "/some/path", Mode: 0755}
		b, err := EncodePathModeRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodePathModeRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
	t.Run("Int", func(t *testing.T) {
		in := IntPayload{Value: -123456}
		b, err := EncodeIntRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodeIntRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
	t.Run("OpenFile", func(t *testing.T) {
		in := OpenFilePayload{Path: "/some/file.txt", Flags: int32(os.O_WRONLY | os.O_CREATE), Mode: 0644}
		b, err := EncodeOpenFileRequest(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecodeOpenFileRequest(b)
		if err != nil {
			t.Fatal(err)
		}
		if in != out {
			t.Errorf("round-trip mismatch: in=%v out=%v", in, out)
		}
	})
}

func TestTruncatedPayloadErrors(t *testing.T) {
	full, err := EncodeMountRequest(MountPayload{
		Source: "/a", Target: "/b", FSType: "tmpfs",
		Flags: 0, Data: "size=1g",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every truncation length from 0 to len(full)-1 must fail to decode.
	for cut := 0; cut < len(full); cut++ {
		if _, err := DecodeMountRequest(full[:cut]); err == nil {
			t.Errorf("truncated to %d bytes: expected error, got nil", cut)
		}
	}
}

func TestTruncatedEnvelopeErrors(t *testing.T) {
	payload, err := EncodeMountRequest(MountPayload{Source: "/a", Target: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	full, err := EncodeRequest(&Request{Cookie: 42, FuncID: FuncMount, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}

	for cut := 0; cut < len(full); cut++ {
		if _, err := DecodeRequest(full[:cut]); err == nil {
			t.Errorf("truncated to %d bytes: expected error, got nil", cut)
		}
	}
}
