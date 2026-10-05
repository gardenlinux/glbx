# Debian format layer

Pure, I/O-free parsing and resolution of Debian metadata.

## `internal/debian/version`

Parses `epoch:upstream-revision`, formats canonically, and compares per
`deb-version(7)` — tilde ordering, alternating non-digit/digit segments — with
constraint checking against an operator. A compatibility test pins the ordering
against known dpkg results.

## `internal/debian/deb822`

A streaming parser for the deb822 stanza format used by Packages, Sources, and
Release files: lower-cased field names, continuation lines, comments, and
blank-line stanza separation over a supplied reader.

## `internal/debian/depends`

Parses dependency relationships — version constraints, architecture qualifiers
and restriction lists, build-profile groups, alternatives, and clause lists —
and provides architecture-wildcard matching and profile-activation filtering.

## `internal/debian/index`

The in-memory binary-package index built from a Packages stream, with
pre-parsed dependency fields, lookup by real and virtual (Provides) name, the
essential set, and merge/subset queries.

## `internal/resolver`

A backtracking dependency resolver: propagation with a decision stack and state
snapshots, virtual packages via providers, conflicts via exclusion, ranked
alternatives, deterministic sorted output, and a structured error tree on
failure.

## `internal/buildcfg`

Host-architecture detection and the typed loaders for the on-disk declarative
files: `build.yml` (the hand-authored per-package declaration), `sources.yml`
(upstream archive pins), `build-deps.yml` / `rootfs-deps.yml` (the explicit
pinned-tooling lists), and the image package list.
