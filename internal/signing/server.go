package signing

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"google.golang.org/protobuf/proto"

	pb "github.com/gardenlinux/glbx/internal/signing/pb"
)

const maxMsg = 1 << 16

// debugLog reports to stderr when GLBX_SIGN_DEBUG is set, matching the forwarding
// module's own switch, so both ends of the socket can be traced together.
var debugEnabled = os.Getenv("GLBX_SIGN_DEBUG") != ""

func dbg(format string, args ...any) {
	if debugEnabled {
		log.Printf("signing-service: "+format, args...)
	}
}

// Serve listens on a SOCK_SEQPACKET Unix socket at socketPath and answers signing
// requests with backend until ctx is cancelled. Each connection carries exactly
// one request and one response. It returns once the listener has stopped and the
// socket file has been removed.
func Serve(ctx context.Context, socketPath string, backend Backend) error {
	os.Remove(socketPath)
	ln, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socketPath, Net: "unixpacket"})
	if err != nil {
		return fmt.Errorf("signing: listen %s: %w", socketPath, err)
	}
	// The build connects as an unprivileged user inside the container, while the
	// socket is created by this host-side service under a different uid. Make the
	// socket connectable regardless of uid: it is a request channel, not a secret,
	// and the private key never crosses it.
	if err := os.Chmod(socketPath, 0666); err != nil {
		ln.Close()
		return fmt.Errorf("signing: chmod %s: %w", socketPath, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			os.Remove(socketPath)
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("signing: accept: %w", err)
		}
		handle(conn, backend)
		conn.Close()
	}
}

func handle(conn *net.UnixConn, backend Backend) {
	buf := make([]byte, maxMsg)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	var env pb.Envelope
	if err := proto.Unmarshal(buf[:n], &env); err != nil {
		return
	}

	var reply *pb.Envelope
	switch m := env.Msg.(type) {
	case *pb.Envelope_PublicKeyRequest:
		dbg("public-key request")
		reply = &pb.Envelope{Msg: &pb.Envelope_PublicKeyResponse{
			PublicKeyResponse: backend.PublicKey()}}
	case *pb.Envelope_SignRequest:
		dbg("sign request: %d digest bytes, hash=%s", len(m.SignRequest.Digest), m.SignRequest.HashAlgo)
		resp := &pb.SignResponse{}
		if sig, err := backend.Sign(m.SignRequest.Digest, m.SignRequest.HashAlgo); err != nil {
			dbg("sign failed: %v", err)
			resp.Error = err.Error()
		} else {
			resp.Signature = sig
		}
		reply = &pb.Envelope{Msg: &pb.Envelope_SignResponse{SignResponse: resp}}
	case *pb.Envelope_CertRequest:
		dbg("cert request: returning %d cert bytes", len(backend.Certificate()))
		reply = &pb.Envelope{Msg: &pb.Envelope_CertResponse{
			CertResponse: &pb.CertResponse{Der: backend.Certificate()}}}
	default:
		dbg("unexpected message type %T", m)
		return
	}

	out, err := proto.Marshal(reply)
	if err != nil {
		return
	}
	conn.Write(out)
}
