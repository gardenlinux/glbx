package ipc

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

const (
	FuncExec       uint32 = 1
	FuncWait       uint32 = 2
	FuncMount      uint32 = 3
	FuncMkdir      uint32 = 4
	FuncCreateFile uint32 = 5
	FuncSymlink    uint32 = 6
	FuncPivotRoot  uint32 = 7
	FuncUmount     uint32 = 8
	FuncRmdir      uint32 = 9
	FuncUnlink     uint32 = 10
	FuncOpen       uint32 = 11
)

// Payload structs. Each FuncID's request and reply maps to one of these.
// The mapping is fixed by protocol position (FuncID), not by gob type tags
// — the wire never carries a type record for the payload.
//
// Request payloads:
//   FuncExec       → ExecPayload
//   FuncWait       → IntPayload (the pid)
//   FuncMount      → MountPayload
//   FuncMkdir      → PathModePayload
//   FuncCreateFile → PathModePayload
//   FuncSymlink    → SymlinkPayload
//   FuncPivotRoot  → PathPayload
//   FuncUmount     → UmountPayload
//   FuncRmdir      → PathPayload
//   FuncUnlink     → PathPayload
//   FuncOpen       → OpenFilePayload
//
// Reply payloads:
//   FuncExec → IntPayload (the spawned pid)
//   FuncWait → IntPayload (the exit code)
//   FuncOpen → (no payload; FD returned via SCM_RIGHTS in response ancillary data)
//   all others → empty (nil bytes)

type ExecPayload struct {
	Argv         []string
	Cwd          string
	Env          []string
	ResetEnv     bool
	HasCreds     bool
	UID          uint32
	GID          uint32
	UnshareFlags uint64
}

type MountPayload struct {
	Source string
	Target string
	FSType string
	Flags  uint64
	Data   string
}

type UmountPayload struct {
	Target string
	Flags  int64
}

type SymlinkPayload struct {
	Target   string
	Linkpath string
}

// PathPayload is shared across FuncRmdir, FuncUnlink, FuncPivotRoot.
type PathPayload struct {
	Path string
}

// PathModePayload is shared across FuncMkdir, FuncCreateFile.
type PathModePayload struct {
	Path string
	Mode uint32
}

// IntPayload is the FuncWait input and the FuncExec / FuncWait reply.
type IntPayload struct {
	Value int64
}

// OpenFilePayload is the FuncOpen request. Flags mirrors os.OpenFile's flag
// parameter (os.O_RDONLY, os.O_WRONLY|os.O_CREATE, etc). Mode is the
// permission bits used when creating a new file (ignored otherwise).
type OpenFilePayload struct {
	Path  string
	Flags int32
	Mode  uint32
}

func encodePayload[T any](p T) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(p); err != nil {
		return nil, fmt.Errorf("ipc: encode %T: %w", p, err)
	}
	return buf.Bytes(), nil
}

func decodePayload[T any](data []byte) (T, error) {
	var p T
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&p); err != nil {
		var zero T
		return zero, fmt.Errorf("ipc: decode %T: %w", p, err)
	}
	return p, nil
}

func EncodeExecRequest(p ExecPayload) ([]byte, error) { return encodePayload(p) }
func DecodeExecRequest(b []byte) (ExecPayload, error) { return decodePayload[ExecPayload](b) }

func EncodeMountRequest(p MountPayload) ([]byte, error) { return encodePayload(p) }
func DecodeMountRequest(b []byte) (MountPayload, error) { return decodePayload[MountPayload](b) }

func EncodeUmountRequest(p UmountPayload) ([]byte, error) { return encodePayload(p) }
func DecodeUmountRequest(b []byte) (UmountPayload, error) { return decodePayload[UmountPayload](b) }

func EncodeSymlinkRequest(p SymlinkPayload) ([]byte, error) { return encodePayload(p) }
func DecodeSymlinkRequest(b []byte) (SymlinkPayload, error) { return decodePayload[SymlinkPayload](b) }

func EncodePathRequest(p PathPayload) ([]byte, error) { return encodePayload(p) }
func DecodePathRequest(b []byte) (PathPayload, error) { return decodePayload[PathPayload](b) }

func EncodePathModeRequest(p PathModePayload) ([]byte, error) { return encodePayload(p) }
func DecodePathModeRequest(b []byte) (PathModePayload, error) {
	return decodePayload[PathModePayload](b)
}

func EncodeIntRequest(p IntPayload) ([]byte, error) { return encodePayload(p) }
func DecodeIntRequest(b []byte) (IntPayload, error) { return decodePayload[IntPayload](b) }

func EncodeOpenFileRequest(p OpenFilePayload) ([]byte, error) { return encodePayload(p) }
func DecodeOpenFileRequest(b []byte) (OpenFilePayload, error) {
	return decodePayload[OpenFilePayload](b)
}
