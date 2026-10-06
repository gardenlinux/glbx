# Execution environment

A hermetic sandbox built directly on Linux namespaces, with its own command-
line entry point. It stands alone — the build engine and the store are built on
top of it, not the other way round.

## `internal/ipc`

A local message-boundary socket transport with file-descriptor passing: gob
request/response framing over a `SOCK_SEQPACKET` pair, a host-side client (one
in-flight call, cookie-matched), and a reactive server. The environment-
resolution rule (overlay vs. reset) the layers share lives here too.

## `cmd/exec_env_stub`

A tiny reactive process that serves the control channel's request set — exec,
wait, mount, mkdir, open (returning a file descriptor), pivot_root, and the
rest — from inside whatever namespace it is launched into.

## `internal/container`

`BaseExecEnv` and `BaseFsContext` run commands and filesystem operations
directly on the host. `RemoteExecEnv` drives one stub over the control channel,
providing both the exec and filesystem interfaces, including FD-returning file
opens. ID-map computation intersects subordinate ranges with the parent map and
reserves inner root for the calling user, applied through the set-uid
`newuidmap`/`newgidmap` helpers. The layered stack — user namespace, mount
namespace, then the pivoted run layer with its PID namespace and base mounts —
is the same channel-backed environment constructed with different unshare flags
and credentials.

The `glbx exec-chroot` command unpacks a stored root filesystem and runs a
command inside the full stack, the direct way to exercise it.
