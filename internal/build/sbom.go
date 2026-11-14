package build

import "context"

// SBOM returns the artifact's serialized SBOM document. These are scaffolding
// stubs: each kind returns a tiny placeholder document for now, and the engine
// wires the write-to-store path around them. Real SPDX generation — the content
// described in doc/src/concepts/sbom.md — replaces these bodies later. An artifact
// kind that should produce no SBOM would return nil here instead.
//
// Keeping all five implementations in one file lets the stubs share a single
// import and be replaced together when real generation lands.

func (s *DebianPkgBuild) SBOM(ctx context.Context) ([]byte, error) { return sbomStub(), nil }

func (b *debianBinaryPkg) SBOM(ctx context.Context) ([]byte, error) { return sbomStub(), nil }

func (r *Rootfs) SBOM(ctx context.Context) ([]byte, error) { return sbomStub(), nil }

func (im *Image) SBOM(ctx context.Context) ([]byte, error) { return sbomStub(), nil }

func (t *ToolingPkgBuild) SBOM(ctx context.Context) ([]byte, error) { return sbomStub(), nil }

// sbomStub is the placeholder SBOM document bytes every kind emits during
// scaffolding.
func sbomStub() []byte { return []byte("{}\n") }
