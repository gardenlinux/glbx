# The Execution Environment

A reproducible build has to run in a known, well-defined environment. A build
host, by contrast, is in whatever state it happens to be in: a particular
distribution, a particular set of installed packages and their versions, stray
tools on `PATH`, environment variables, configuration under the user's home.
If a build could see any of that, its result would depend on it — and the same
sources would produce different outputs on different machines. The **execution
environment** exists to sever that dependency: it gives each build a clean,
precisely-defined world containing only its declared inputs, so the output is a
function of the inputs and nothing else.

Running Debian tooling cleanly adds a second constraint. `dpkg`,
`dpkg-buildpackage`, compilers, and the maintainer scripts packages ship all
expect to operate as root — they install into a root filesystem, change file
ownership, and run package-defined code during configuration. A build must be
able to present that root-owned world to the tooling.

The twist is that glbx must do all of this **unprivileged**. Constructing these
clean environments cannot require actual root on the build box: a regular,
unprivileged user has to be able to run the whole build system. That rules out
anything that depends on elevated privileges to set up, and it is why the sandbox
is built the way it is.

The guiding requirements, in priority order:

- **Hermetic.** A build sees only its declared inputs. The host's installed
  software, environment, and configuration are not visible, so they cannot leak
  into the result. This is the primary reason the environment exists.
- **Constructible unprivileged.** An ordinary user can stand up the environment
  with no real root on the host. glbx itself never needs to be privileged.
- **Root inside.** Within that environment the tooling nonetheless gets to be
  root — enough to install packages into a fresh root filesystem and set
  ownership — because that is what unmodified Debian tooling expects.
- **The host is left untouched.** Whatever is set up for a build does not outlive
  it or leak back onto the host. This also keeps the host safe from the arbitrary
  code a build may run, but that protection is a consequence of hermeticity, not
  its purpose.
- **The kernel is the only dependency.** No Docker, no Podman, no
  `systemd-nspawn`. glbx does not get to prescribe what container runtime a
  developer or CI machine has installed; it relies only on facilities the Linux
  kernel already provides.

The clean environment is a property the system *constructs*, not a convention a
build is trusted to honor. A build cannot opt out of it any more than it can opt
out of its identity.

## The kernel primitives, and why each

The sandbox is assembled from three Linux namespaces. They are orthogonal — each
isolates a different kind of resource — and each earns its place for a specific
reason. glbx uses exactly these three, and deliberately not others.

### User namespace — the foundation

A user namespace lets a process hold a set of user and group IDs that are
*remapped* relative to the host. Inside the namespace a process can be UID 0 —
root — while outside it is just the unprivileged user who started glbx.

This is what makes everything else possible unprivileged. Mounting, pivoting
into a new root, and chowning files during package installation are all
root-only operations; the user namespace is what lets the sandbox perform them
without the host user ever actually being root.

The mapping has a specific shape. The host user is granted a *subordinate* range
of IDs it is allowed to hand out inside namespaces (the system records these
ranges per user; applying them to a namespace is done through the standard
setuid id-map helpers, since an unprivileged process cannot widen its own id
range by itself). glbx builds a map that looks like:

```
inner 0            → host uid of the calling user     (count 1)
inner 1 .. N       → start of the subordinate range … (count N)
```

Inner UID 0 is reserved for the calling user deliberately. "Root inside maps to
*me* outside" is what lets the sandbox pivot into a root filesystem the host user
owns and operate on files there; if inner 0 mapped to some anonymous subordinate
ID instead, the very first privileged setup step would fail. The subordinate
range above it gives the sandbox a full complement of other UIDs/GIDs, so tooling
that creates users or installs files owned by non-root accounts behaves normally.

With the map in place, a process that thinks it is root can do everything root
can do *with respect to the namespace's own resources* — which is precisely the
set of things a package build needs.

### Mount namespace — a private filesystem view

A mount namespace gives a process its own mount table. Bind mounts, tmpfs mounts,
unmounts, and changes to mount propagation all take effect only inside it. This
is the workbench: it is where a build's inputs are staged into place and where
its outputs are harvested, all without a single mount becoming visible to the
host.

### PID namespace — a clean process tree

A PID namespace makes the first process inside it PID 1, with its own numbering
below. The payoff is a clean, self-contained process tree per build, and
satisfaction of tooling that assumes it was started by init. It is the last layer
applied, wrapping the process that actually runs the build.

### What is deliberately left out

- **No network namespace.** The build chroot shares the host's network. This is
  intentional: resolving and fetching build-time tooling can need network access
  at build time, and the from-source and dependency-locality guarantees do not
  come from severing the network — they come from building against pinned,
  content-verified inputs and from validating each produced binary's closure (see
  [package-build.md](./package-build.md)). Cutting the network would buy no
  correctness here and would break tooling that reaches out legitimately.
- **No UTS, IPC, time, or cgroup namespace.** None of them change the correctness
  of a package build. Leaving them out keeps the sandbox small and its behavior
  easy to reason about.

These omissions describe the current scope, not a permanent boundary. If a later
need makes one of them worth its complexity, it slots in as one more orthogonal
property.

### `pivot_root`, not `chroot`

Giving the build its own root filesystem is done with `pivot_root`, not `chroot`.
`chroot` merely moves a process's idea of `/`; the old root remains mounted and
reachable, and references to it can linger. `pivot_root` swaps the root out of
the mount table entirely, after which the old root can be detached outright — so
no process inside retains any path to the filesystem outside its new root. It is
the stronger guarantee, and it is the one the sandbox depends on. (`pivot_root`
requires the new root to itself be a mount point; the setup arranges that before
pivoting.)

## Layering: one property per step

The sandbox is not a single monolithic environment. It is a **stack**, where each
layer adds exactly one isolation property over the layer beneath it:

```
host execution            (no isolation — a plain child process on the host)
  └─ user namespace        (remapped IDs: root inside, unprivileged outside)
       └─ mount namespace   (a private mount table to stage into)
            └─ pivoted run   (PID namespace + pivot_root into the build root)
```

Each layer presents the *same* interface as the layer below it (described next),
so a consumer runs work through a handle without needing to know which layer it
is really talking to. Composition is literal: the bottom layer launches the next
as a child placed into a fresh namespace, that one launches the next, and a
request to run something is forwarded down the stack until it lands in the
innermost environment and executes there.

## Why mount and pivot are separate layers

The most important structural decision is that the **mount** layer and the
**pivoted run** layer are distinct, rather than folded into one "give me a
container" step. Keeping them apart is what lets glbx stage a build's inputs and
collect its outputs *from outside the chroot*. Walking through how an image root
filesystem is actually assembled makes the reason concrete.

1. **Stage inputs in the mount namespace.** At this layer the host's paths are
   still visible — including the object store. The built `.deb` blobs the build
   needs are bind-mounted, read-only, into fixed locations under the staged root
   filesystem. Bind-mounting rather than copying matters: a `.deb` can be
   hundreds of megabytes, and the store already holds the exact bytes under their
   content hash (see [object-store.md](./object-store.md)); a bind mount makes
   them appear in place at zero copy cost. The build tmpfs and the base layer are
   set up here too.

2. **Pivot only the run step.** Only the innermost layer enters a PID namespace
   and pivots into the assembled root filesystem to actually run `dpkg` and the
   maintainer scripts. That process is now sealed inside the chroot as PID 1.

3. **Harvest the result from the mount namespace — not the chroot.** When
   installation finishes, the finished tree has to be read back out and packed
   into a deterministic tar. That read happens in the mount-namespace layer,
   where the tree is just a directory on a tmpfs, fully visible. The packer never
   enters the chroot or the PID namespace.

Mount propagation is tuned so this one-way visibility holds: the mount namespace
is the *producer* of mounts that appear inside the pivoted environment, but
mounts the pivoted environment makes for itself never propagate back out
(slave-style propagation, with fully private as a weaker fallback when a kernel
configuration rejects it — at the cost of input injection no longer showing
through).

The payoff, stated plainly: **because inputs are staged and outputs are harvested
in the mount namespace while only the run step is pivoted, the orchestrator can
read the build's filesystem and pack its result without ever being trapped inside
the chroot or the PID namespace.** A single combined environment would make the
staging and the harvesting impossible to do from the outside — you would be
inside the sealed root, unable to reach the store to mount inputs or to stand
outside the tree to pack it.

This is also why many steps stop at the mount-namespace layer and never build the
pivot layer at all. Anything whose job is to arrange a filesystem and run tooling
against host-visible paths — extracting packages, packing a tar — needs the
private mount table but not the sealed chroot. The pivoted layer is constructed
only when something must actually execute code *as if it were* the final root.

## The two interfaces

Work with execution environments goes through two interfaces, split by concern.

**ExecEnv** is process execution. It provides exactly three operations:

- **exec** — start a process from an argv (with its working directory,
  environment, credentials, and the file descriptors to wire to its stdio),
  returning a handle to the running process.
- **wait** — block on a handle until that process exits, returning its exit
  status.
- **close** — tear the environment down, releasing whatever backs it.

The layering works because *every layer is itself an ExecEnv* — "exec" means
"start the process inside whatever isolation this layer provides" — so the layers
nest without any of them being special-cased.

**FsContext** is a filesystem *view*. It provides the operations that arrange and
read a filesystem:

- **mkdir / create-file / symlink** — make directories, empty files, and symlinks.
- **rmdir / unlink** — remove them.
- **mount / umount** — attach and detach mounts (bind mounts, tmpfs, propagation
  changes).
- **open** — open a path and return a live file handle for ordinary read/write.
- **mktempdir** — create a uniquely-named temporary directory.

Opening a path returns a live file handle *into* the view rather than streaming
bytes across any channel: once the handle exists, reads and writes go straight
through the kernel. The pivot into a new root is pointedly **not** one of these
operations — it is a one-time act of building the innermost layer, not a routine
view operation, so it lives outside this interface.

## Which implementations provide which interface

The two interfaces exist separately because the set of implementations that
satisfy each is genuinely different — and because nesting only ever needs an
ExecEnv, while a filesystem view is sometimes wanted on its own.

There are two **host-direct** implementations, and they are distinct objects:

- A host **ExecEnv** that runs processes *directly in the glbx process itself* —
  a plain fork/exec, no helper process, no channel, no isolation. `wait` is a
  direct wait on that child; `close` has nothing to release. This is the bottom
  of every stack.
- A host **FsContext** that performs filesystem operations *directly in the glbx
  process* with ordinary system calls — again no helper, no channel. It is used
  by build phases that legitimately touch host paths before any namespace exists.

Note that the host ExecEnv is **exec-only**: it is not an FsContext. On the host
there is no reason to route file operations through an "environment" at all — a
caller that wants host filesystem access just uses the host FsContext. The two
host implementations are independent; neither needs the other.

Every isolated layer, in contrast, is backed by **one shared implementation** — a
*remote exec env* that drives a single helper process in a namespace over a
channel (described next). That one implementation provides **both** interfaces:
once a helper is running inside a namespace, the same helper can both run
processes there and perform filesystem operations there, so exposing exec and fs
through the same backing object is natural and costs nothing extra.

The three isolated layers are not three separate implementations. They are the
*same* remote-exec-env object, constructed with different unshare flags and
credentials:

| Layer | Unshare flags | Credentials | Useful interfaces |
|-------|---------------|-------------|-------------------|
| user namespace | new user ns | — (gets inner root automatically) | ExecEnv only |
| mount namespace | new mount ns | inner root | ExecEnv + FsContext |
| pivoted run | new mount + PID ns | inner root | ExecEnv + FsContext |

The user-namespace layer is the subtle case. Because it owns a user namespace but
*not* a mount namespace, it is useful only as an ExecEnv — a thing to nest the
mount layer inside. It technically carries the FsContext operations (it shares
the one remote-exec-env implementation), but a `mount` or `mkdir` issued against
it would act on a mount table it does not privately own, so those operations are
not meaningfully usable at that layer. In practice the user-namespace layer is
treated as ExecEnv-shaped: you nest a mount layer on top before doing filesystem
work.

The filesystem operations could just as well have been folded into ExecEnv — the
remote-exec-env object answers both over the same channel to the same helper.
They are split into their own interface mostly to keep each interface minimal: a
caller that only needs to nest or run processes depends on ExecEnv alone, and a
caller that only arranges files depends on FsContext alone, without either
dragging in operations it never uses.

So the full picture:

- host ExecEnv — exec only, in-process, no isolation.
- host FsContext — filesystem view only, in-process, no isolation.
- remote exec env — both interfaces, over a channel to one helper; the single
  implementation behind all three isolated layers, which differ only in the
  namespaces they create and the credentials they run with.

## Delegating into a namespace

A namespace can only be entered at the moment a process is created — there is no
"enter this existing namespace with my whole runtime" that is safe for a
multi-threaded program, because unsharing applies to the calling thread, not the
whole process. So glbx does not try to step into a namespace itself. Instead,
each isolated layer is a fresh, single-purpose **helper process** spawned
directly into the new namespace — by `exec`-ing it through the layer below with
the right unshare flags set — and the orchestrator drives it from outside over a
small control channel. This is why nesting only needs an ExecEnv underneath: a
layer is created by running one process through the environment below it.

The channel is a local socket that preserves message boundaries, is ordered and
bidirectional, and — crucially — can pass file descriptors across itself. FD
passing is what lets the orchestrator hand the helper the standard input/output
streams (or pipes) for each process it runs, and what lets an `open` inside the
namespace return a usable handle back out.

The request set the helper understands is small: run a process, wait on one, the
filesystem-view operations, and the one-time pivot. The helper is purely
reactive — the orchestrator issues requests and the helper replies; the helper
never initiates anything. Because it is already *inside* the right namespace,
every operation it performs takes effect there, and the orchestrator never has to
enter the namespace to get work done in it. Closing the channel is the shutdown
signal; the helper exits, and teardown proceeds from the innermost layer outward.

Keeping the helper tiny and reactive keeps the privileged surface of the whole
system concentrated in one small, auditable place.

## What goes through the sandbox — and what need not

The rule is: **anything that changes filesystem state, runs maintainer scripts,
or executes package-defined code goes through an ExecEnv.** That is the entire
point — those are the operations whose result depends on the environment they run
in, so they must run in the clean, well-defined one rather than against the
host's undefined state. Reaching around the interface to "just run one quick
command directly" silently runs that operation against the host instead, and
discards the hermeticity the surrounding build believed it had.

Pure data-in / data-out helpers are the deliberate exception. Operations that
only transform a byte stream — decompression, signature verification, extracting
an archive to a stream — depend only on their input bytes, not on any ambient
environment, so running them directly on the host cannot change their result.
The boundary is drawn at exactly that line: a pure function of its input bytes
may be direct; anything that reads or mutates a filesystem, or runs foreign code,
goes through the environment.

## Future outlook: swappable backends

Everything above is built on Linux namespaces, and for now the bottom of the
stack — the layer that ultimately runs a process on the real host — is a direct
host fork/exec. But that bottom layer sits behind the same ExecEnv/FsContext
interfaces as every layer above it, and nothing in the artifact or build code
knows or cares what it is.

That leaves room to grow. A future backend could place something other than a
plain host process behind those interfaces — a lightweight virtual machine via
Virtualization.framework on macOS, or a Linux environment under WSL on Windows —
and ship requests to it instead of forking locally. The build engine, the
artifact graph, and the staging logic would be unchanged; only the base of the
stack would differ. This is a direction the interface is deliberately shaped to
*allow*, so that glbx is not permanently confined to Linux hosts. It is not
something the current design sets out to deliver — the point for now is simply
that the seam is in the right place.
