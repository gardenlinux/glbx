# Debian format layer

This chapter covers the pure, I/O-free Debian metadata layer under
`internal/debian/` and the on-disk configuration loaders under
`internal/buildcfg/`. Together they form the parsing and representation
substrate the rest of glbx reasons over: version ordering, the deb822 stanza
grammar, dependency-relationship parsing with architecture and profile
filtering, the in-memory binary-package index, and the typed Go views of every
declarative YAML file in a config repo.

Everything here is deterministic and (with the single exception of
`HostArch`, which shells out to `dpkg`) touches no external state beyond the
bytes or files it is handed. The backtracking dependency resolver that consumes
the index is a separate component — see its own chapter.

## `internal/debian/version` — deb-version(7) ordering

`version.go` implements Debian version parsing, formatting, and comparison per
`deb-version(7)`. A version decomposes into three parts:

```go
type Version struct {
    Epoch    int
    Upstream string
    Revision string
}
```

`Parse` splits a version string `[epoch:]upstream[-revision]` by structure, not
by validating character classes: the epoch is everything before the first `:`
(parsed as a non-negative integer, defaulting to `0` when absent and rejected
when negative or non-numeric); the revision is everything after the **last** `-`
(empty when there is no `-`); the upstream version is what remains and must be
non-empty. `String` is the inverse: it omits a zero epoch and an empty revision,
so `Parse`→`String` round-trips to the canonical form.

### Comparison algorithm

`Version.Compare` returns `-1`/`0`/`+1` and applies the three-stage deb-version
rule in order: epochs numerically first, then upstream, then revision. Upstream
and revision are each compared by `compareDebianString`, which is the subtle
part and the place to be careful.

The algorithm walks both strings in lockstep, alternating between a **non-digit
segment** and a **digit segment** until both strings are exhausted:

- **Non-digit segments** are compared character by character using a special
  ordering, *not* plain ASCII. The order is: `~` sorts before everything
  (including the empty string / end of segment), then the empty string, then
  ASCII letters (`a`–`z`, `A`–`Z`), then every other byte. `charOrder` encodes
  this: `~` → `-1`, end-of-segment → `0`, a letter → its ASCII value (`1..`), and
  any other byte → ASCII `+256` so it sorts strictly after all letters. This is
  what makes `1.0~rc1` sort *before* `1.0`, the central tilde rule used for
  pre-releases.
- **Digit segments** are compared numerically via `compareDigitPart`: leading
  zeros are stripped, an empty or all-zero segment normalizes to `0`, then the
  longer digit string (after stripping) is the larger number, with equal lengths
  broken by lexical comparison of the digits.

A correctness point worth internalizing: "letter" means ASCII letter only. Bytes
`>= 0x80` (e.g. UTF-8 continuation bytes, Latin-1 accented characters) are
treated as non-letter, non-tilde and therefore sort *after* all letters — exactly
how dpkg behaves. `ascii_test.go` pins this down (`TestCharOrderASCIIOnly`,
`TestCompareNonASCIIBytes`), because a naive `unicode.IsLetter` would silently
diverge from dpkg here.

### Package-level helpers and constraints

`Compare(a, b string)` parses both operands and compares them, falling back to
plain lexical comparison when either string is unparseable (rather than
panicking), so a malformed version never crashes a caller.

`CheckConstraint(version, op, constraint)` evaluates a relational dependency
operator against two version strings. `op` must be one of the five Debian
operators `>>`, `>=`, `=`, `<=`, `<<`; any other operator is an error. It is a
thin wrapper over `Compare` plus the comparison sign.

The package is validated against real dpkg behaviour: `version_test.go`'s
`TestDpkgCompatibility` carries version pairs whose ordering was confirmed with
`dpkg --compare-versions`, and dedicated tests cover tilde edge cases, numeric
segments, and the empty-revision-equals-zero rule. When touching this code,
extend those tables rather than reasoning about the algorithm in the abstract.

## `internal/debian/deb822` — the streaming stanza parser

`parser.go` is a streaming reader for the deb822 (RFC-822-like) format used by
`Packages`, `Sources`, and `Release` files. A stanza is just a field map:

```go
type Stanza map[string]string
```

Field names are normalized to **lowercase** on the way in, so every consumer
looks up `stanza["package"]`, `stanza["sha256"]`, etc. — never the mixed-case
form from the file. Values retain their original case.

`NewReader` wraps an `io.Reader` in a `bufio.Scanner` with a 1 MiB line buffer
(large `Description`/`Checksums` fields would overflow the default). `Next()`
returns one stanza at a time and reports `io.EOF` when the input is exhausted —
the standard streaming loop is `for { s, err := r.Next(); if err == io.EOF { break } ... }`.

Parsing rules enforced per line:

- Lines beginning with `#` are comments and skipped entirely.
- A blank (or whitespace-only) line terminates the current stanza; leading blank
  lines between stanzas are swallowed so an empty stanza is never returned.
- A line beginning with a space or tab is a **continuation** of the previous
  field; its text is appended with an embedded newline, preserving multi-line
  field structure. A continuation with no preceding field is an error.
- Otherwise the line is a `Name: Value` field. The name is validated
  (`validateFieldName`: non-empty, printable ASCII, no colon, no whitespace) and
  lowercased; the value is everything after the colon with a single leading space
  stripped.

The last field's value has its trailing newline trimmed when the stanza closes,
so single-line fields never carry a stray `\n`. The reader is one-shot and
forward-only; there is no seek. A small internal peek buffer exists
(`peeked`/`hasPeek`) but `Next` currently drives the scanner directly.

## `internal/debian/depends` — relationship parsing and filtering

This package parses the dependency-expression grammar shared by `Depends`,
`Pre-Depends`, `Build-Depends`, `Conflicts`, `Provides`, `Breaks`, and friends,
and then provides the architecture- and profile-matching predicates that decide
whether a given relation applies to the build at hand. It is split across
`depends.go` (grammar) and `filter.go` (predicates).

### The parsed shape

A dependency field is a two-level structure: comma-separated **clauses**, each of
which is a `|`-separated set of **alternatives**:

```go
type Dependency struct {
    Name        string             // package name
    Arch        string             // arch qualifier after ':' (pkg:amd64)
    Version     *VersionConstraint // optional (op version)
    ArchList    []string           // [amd64 arm64] or [!i386]
    ArchExclude bool               // true when the list is an exclusion (leading !)
    Profiles    [][]string         // <cross> <!nocheck> → groups of terms
}

type Alternative []Dependency     // joined by |
type DependencyList []Alternative // clauses joined by ,
```

`VersionConstraint` holds an `Op` (one of the five Debian operators) and the
`Version` string.

`Parse` splits the input with `splitTopLevel`, which respects nesting: it only
breaks on a delimiter when not inside parentheses, square brackets, or angle
brackets. This matters because a version constraint `(>= 1.0)`, an architecture
list `[amd64 arm64]`, and a profile group `<!nocheck>` all contain characters
that would otherwise be mistaken for structure. Each atom is then parsed by a
small position-based `parser`:

- **Package name** — lowercase alphanumerics plus `+`, `-`, `.`.
- **Arch qualifier** — an optional `:arch` suffix on the name, stored in `Arch`
  (distinct from `ArchList`; this is the `pkg:amd64` multiarch qualifier, not a
  restriction).
- **Version constraint** — `(op version)`, operator validated against the five
  legal operators.
- **Architecture restriction** — `[...]`, a space-separated list of arch tokens;
  if the *first* token is `!`-prefixed the whole list is treated as an exclusion
  (`ArchExclude = true`). The `!` prefix is stripped from the stored tokens.
- **Build profiles** — zero or more `<...>` groups, each a space-separated list
  of profile terms that may be `!`-negated. Multiple groups accumulate into
  `Profiles`.

### Architecture matching

`ArchMatches(pattern, arch)` implements Debian arch wildcards used in restriction
lists:

- `any` matches everything.
- An exact string match.
- A `<os>-any` suffix matches when the OS is `linux` (glbx targets Linux only, so
  `linux-any` matches and any other `*-any` OS does not).
- An `any-<cpu>` prefix matches any OS for that CPU — currently accepted
  unconditionally.

`Dependency.MatchesArch(arch)` layers the inclusion/exclusion logic on top: an
empty `ArchList` always matches; an exclusion list matches unless some entry
matches `arch`; an inclusion list matches only when some entry matches.

### Profile activation

`Dependency.ExcludedByProfiles(activeProfiles)` applies Debian's build-profile
rule. A dependency with no profile groups is never excluded. Otherwise it is
**included** precisely when at least one group is *satisfied*, and a group is
satisfied only when **every** term in it matches the active set: a plain term
`foo` requires `foo` to be active, a negated term `!foo` requires `foo` to be
*inactive*. If no group is satisfied, the dependency is excluded. The method
returns `true` for "excluded", so a resolver typically keeps dependencies for
which `ExcludedByProfiles(active)` is `false` and that also pass `MatchesArch`.

## `internal/debian/index` — the in-memory binary-package index

`index.go` builds an in-memory `Index` from a deb822 `Packages` stream and serves
the lookups the resolver needs. Each entry is a `Package`:

```go
type Package struct {
    Name, Version, Architecture string
    Essential                   bool
    Priority                    string
    Depends, PreDepends         depends.DependencyList
    Conflicts, Provides, Breaks depends.DependencyList
    Stanza                      deb822.Stanza
    SHA256, SHA1, Filename      string
    Size                        int64
}
```

`ParsePackageFromStanza` is the field extractor. It requires a `Package` field
(error otherwise), reads `version`/`architecture`/`priority`/`filename`
directly, sets `Essential` when the `essential` field case-insensitively equals
`yes`, and pre-parses the five relationship fields (`depends`, `pre-depends`,
`conflicts`, `provides`, `breaks`) into `DependencyList`s so the resolver never
re-parses at lookup time. `SHA256` prefers the `sha256` field and falls back to
`checksums-sha256`. `Size` is parsed leniently — an unparseable size is left as
zero rather than failing the whole stanza.

Two fields are *not* read from the index text. `SHA1` is computed later from the
fetched `.deb` bytes (the `Packages` format carries no SHA1 and it keys the file
in the snapshot archive), and `SourceName()` derives the source-package name on
demand from the `Source:` stanza field — stripping an optional `(version)`
suffix and falling back to the binary package's own name when the field is absent
(a binary with no `Source:` line was built from a source of the same name).

### Lookups and virtual packages

The index keeps three structures: `packages` (name → `*Package`), `providers`
(virtual name → packages that `Provide` it), and `all` (insertion order,
preserved so iteration is deterministic). `Get`/`Has` resolve a **real** package
name; `Providers(name)` returns every package whose `Provides` field names that
virtual package. Virtual-package resolution is therefore a separate query from
real-name lookup, and a single real package can appear under many provider keys.
`EssentialPackages()` returns the `Essential: yes` set — the floor every resolve
must include.

`Load` builds the index from a reader and enforces one hard invariant:
**duplicate real package names are rejected** with an error naming both versions.
A `Packages` stream is expected to carry one stanza per package name; a duplicate
signals a malformed or merged-wrong index, so it fails loudly rather than
silently picking a winner.

### Composition

Three methods compose indices without mutating their inputs (except `Add`):

- `Add(pkg)` inserts or replaces in place, keeping `all` and `providers`
  consistent.
- `Merge(other)` returns a **new** index where `other`'s entries override `idx`'s
  on name collision; the providers map is rebuilt from scratch so stale provider
  edges never linger.
- `Sub(names)` returns a new index restricted to the named packages, silently
  skipping names not present.

`Merge` and `Sub` are the tools for layering a locally built set over a mirror
snapshot and for narrowing an index to a resolve's closure.

## `internal/buildcfg` — host arch and the on-disk schemas

`buildcfg` is the neutral package that holds the Go view of every declarative
file in a config repo, plus host-architecture detection. It is kept dependency-
light on purpose so any package can consume these shapes without creating import
cycles. Loaders follow a consistent convention: a missing file is reported via
`os.IsNotExist` (or a zero value / `nil`) where absence is legitimate, and a
present-but-malformed file is a hard parse error.

### Host architecture

`arch.go`'s `HostArch` returns the Debian architecture name for the host,
memoized with a `sync.Once`. `dpkg --print-architecture` is authoritative when
available; otherwise `GOARCH` is mapped to the Debian vocabulary
(`386`→`i386`, `arm`→`armhf`, `ppc64le`→`ppc64el`, and the identity cases). The
mapping exists because Go's `GOARCH` and Debian's arch names diverge, and the
result is used as the *default* `--arch` so the native architecture is never
silently emulated.

### `build.yml` — the hand-authored per-package declaration

`BuildYML` is the only hand-authored package schema. `LoadBuildYML(pkgDir)` reads
`build.yml` and returns the zero value when the file is absent. Its YAML keys:

- `pkg_type` (`PkgType`) — selects the lockfile provider the `pkg:` generator
  dispatches to (e.g. `debian`, `debian-kernel`). Required to lock through the
  `pkg:` forwarder (empty is an error); no effect on the build itself.
- `gen_control_pkgs` (`GenControlPkgs`) — extra packages placed in the
  control-generation container on top of the essential set; consumed only by
  providers that regenerate a control file before locking (`debian-kernel`).
- `build_profiles` (`BuildProfiles`), `build_options` (`BuildOptions`),
  `extra_build_env` (`ExtraBuildEnv`) — profile list, option list, and extra
  environment entries for the build.
- `build_depends` (`BuildDepends`) — outputs of other local source builds needed
  in this package's build chroot, each a `<source>:<binary>` pair.
- `runtime_depends` (`RuntimeDepends`) — a map from an output binary name to the
  locally built binaries (`<source>:<binary>`) that belong in its runtime
  closure.
- `lockfile_deps` (`LockfileDeps`) — a map from an output binary name to external
  package names tolerated in that binary's validation (satisfied from pinned
  tooling rather than built locally). Per-binary, not inherited.
- `signing` (`Signing`) — a list of `SigningKey` entries requesting
  out-of-container signing. Each carries `key_name` (`KeyName`, the socket mount
  point), `private_key_file` (`PrivateKeyFile`, host-side PEM, never entered into
  the container), and `cert_file` (`CertFile`, the matching X.509 cert, folded
  into the build identity and served over the signing socket).

#### The reduced tooling schema

`ValidateToolingBuildYML(b)` enforces the narrower schema used by assembly-tool
packages under `images.d/tooling/`. A tooling package is a Debian source build
and nothing more — no runtime closure, no install/locality validation, no
signing — so it rejects any `build.yml` declaring `runtime_depends`,
`lockfile_deps`, or `signing`. It does not parse a separate file; it validates an
already-loaded `BuildYML`.

### `sources.yml` — pinned upstream archives

`LoadSourcesYML(pkgDir)` reads the generated source pins into `[]SourceArchive`,
each `{File, Hash, URLs}`: the archive filename, its content hash (an
`objstore.Hash` parsed from the YAML `sha256` string — the identity), and the
ordered retrieval URLs used **only** on a cold cache. The YAML shape is a
top-level `sources:` list of `{file, sha256, urls}`. A missing file surfaces as
`os.IsNotExist`, which callers read as "native package, no external archives".

### `build-deps.yml` / `rootfs-deps.yml` / `tooling-deps.yml` — pinned tools

These three generated lock files share one shape and one loader. `LoadBuildDeps(path)`
(invoked with whichever path) returns `[]PinnedTool`, each `{Name, Version, Files}`
— the external package name, its exact Debian version, and one `PinnedFile` per
architecture variant. A `PinnedFile` is `{Arch, Hash, URLs}` where `Arch` is a
Debian arch or `all` for arch-independent packages, `Hash` is the content hash,
and `URLs` are cold-cache retrieval locations. The YAML shape is a top-level
`tools:` list of `{name, version, files: [{arch, sha256, urls}]}`.

`FilesForArch(tools, arch)` is the selector the installers use: for each tool it
returns the file whose `Arch` equals the target arch *or* is `all`, flattening
the per-tool variants into the exact set to install for one architecture.

### `rootfs-<label>.yml` — rootfs variants

Each top-level `rootfs-<label>.yml` declares one rootfs variant through a single
`packages:` list. `DiscoverRootfsLabels(dir)` globs `rootfs-*.yml`, strips the
`rootfs-` prefix and `.yml` suffix to recover each label, and returns them
sorted. It **excludes** the reserved label `deps` (that file, `rootfs-deps.yml`,
is the shared configuration-tooling lock loaded via the `PinnedTool` path above,
not a variant) and any empty label. `LoadRootfsLabel(dir, label)` reads one
variant's package list, returning `nil` for a missing or malformed file.

### `images.yml` — the image layer

`LoadImages(confDir)` reads the top-level `images.yml` into `[]ImageSpec`. A
missing file is **not** an error (the config repo simply has no image layer) and
yields `nil`. Each `ImageSpec` is one concrete image:

- `name` (`Name`) — the developer-chosen artifact label, surfaced as the graph
  node `image:<name>:<arch>`.
- `rootfs` (`Rootfs`) — a `rootfs-<label>.yml` label.
- `type` (`Type`) — an `images.d/<type>/` directory.
- `inputs` (`Inputs`) — a list of `ImageInput`, material staged into the assembly
  container.

`LoadImages` validates each entry: `name`, `rootfs`, and `type` must all be set;
each input must have a `label`; and input labels must be unique within an image.

`ImageInput` encodes a **one-of** constraint that is the subtle part here:

```go
type ImageInput struct {
    Label string  `yaml:"label"`
    Value *string `yaml:"value"`
    Image string  `yaml:"image"`
}
```

Exactly one of `value` or `image` must be set. A `value` is a literal string
written as a regular file; an `image` names another image whose output is
bind-mounted read-only and which becomes a build dependency. `Value` is a
**pointer** on purpose: it lets an explicit empty string (`value: ""`) be
distinguished from an unset field, so an intentionally empty input is legal while
omitting both fields is not. The validation checks `in.Value != nil` against
`in.Image != ""` and rejects the entry when both or neither is present
(`hasValue == hasImage`).

### `images.d/<type>/conf.yml` — image types

`LoadImageTypeConf(typeDir)` reads an image type's `conf.yml` into `ImageTypeConf`,
the two lists of assembly tools the type's container must contain:

- `debian_tooling` (`DebianTooling`) — mirror binary-package names resolved and
  pinned with their runtime closure into the type's generated `tooling-deps.yml`,
  on top of the essential floor.
- `local_tooling` (`LocalTooling`) — locally built tooling in `<source>:<binary>`
  form pointing into `images.d/tooling/<source>`; these are graph edges to the
  tooling build node, overlaid on the mirrored tools rather than mirror pins.

The loader validates that every `local_tooling` entry is well-formed
`<source>:<binary>` (both halves non-empty). The assembly script itself
(`images.d/<type>/build`) is run by its shebang and is not named in `conf.yml`.

`DiscoverImageTypes(confDir)` lists the subdirectories of `images.d/` that carry
a `conf.yml`, sorted, **excluding** the reserved `tooling/` tree (which holds the
build-only assembly-tool packages, not image types). A missing `images.d/`
yields `nil`.
