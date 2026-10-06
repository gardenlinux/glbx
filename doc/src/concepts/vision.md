# Vision

GardenLinux is an image-centric, immutable operating system. Its deliverable is
a finished system image — a Kubernetes node, a VM host, a container base — in
which every package, configuration, and version is decided at build time. The
image does not mutate in the field: no `apt-get upgrade` at runtime, no drift
between what was tested and what runs.

**glbx** (GardenLinux Build eXecutor) exists to produce those images. It is a
build system that turns version-controlled source packages into immutable,
reproducible system images, with full traceability from the final artifact back
to the exact inputs that produced it.

## Goals

- **From source.** Every binary that lands in a shipped image is compiled from
  source by this build system. No mirrored binaries leak into the output.
- **Reproducible.** Identical inputs produce identical outputs, independent of
  when or where the build runs. A rebuild of unchanged inputs is a cache lookup,
  not recomputation.
- **Hermetic.** A build sees only its declared inputs. The host is shielded from
  the build and the build from the host, so results do not depend on the state
  of the machine that happens to run them.
- **Traceable.** Any file in any image can be followed back through the artifact
  that produced it to the version-controlled source and the exact external
  inputs it was built from.

These are not aspirations layered on afterward; the architecture is chosen so
that they hold by construction.

## Where the ideas come from

Two ecosystems inform the design, and glbx deliberately takes from each the
part the other lacks:

- **Debian** supplies the runtime world: a vast, mature package ecosystem,
  `dpkg`-managed file ownership, a familiar FHS layout, glibc and the GNU
  userland, and stable ABI conventions. glbx keeps all of this. It builds
  Debian-format packages and ships a system that behaves like a normal Linux
  system in every way that matters.

- **Nix-style build systems** supply the discipline: builds as pure functions
  of their inputs, content-addressed identity, isolation as a first-class
  invariant, and reproducibility by construction rather than by convention. glbx
  adopts this rigor around inputs, identity, and caching while keeping the Debian
  runtime world above — its own content-addressed store under a normal
  `dpkg`-managed system.

The combination is the whole point: Debian's leverage with Nix's guarantees.

## What reproducibility means here

Reproducibility is bounded by the packages themselves — if a package's own build
embeds a timestamp into its output, no wrapper can prevent that. What glbx
guarantees is that the *inputs* to every build are fully determined: the source,
the exact set of build-time tooling, and the identities of every dependency. Any
remaining non-determinism is a property of a specific package, and it is
observable (its output hash differs) rather than silent.

## What building from source means here

Two populations of binaries exist during a build and must not be confused:

- **Build-time tooling** — compilers, helpers, and `-dev` headers used *while*
  building a package. These need not be built from source by us: they are
  external inputs, pinned exactly by content hash, and it is fine to retrieve
  them from a third party such as Debian's repositories. They end up in no
  image.
- **Output binaries** — everything produced by our own source builds and
  consumed downstream, including everything in a shipped image. These must,
  transitively, be buildable entirely from sources we control.

Image assembly is the gate that enforces the distinction: if an image's runtime
closure contains a binary we did not build from source, the build fails. What
ships is exactly what was built from controlled sources.
