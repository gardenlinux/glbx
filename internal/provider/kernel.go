package provider

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/install"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
)

func init() {
	RegisterLockGenerator(debianKernelLock{})
}

// debianKernelLock locks a package whose debian/control is a generated artifact.
// Before the ordinary Debian lock path can parse control, this provider
// regenerates it from the package's own templates and defines: the committed
// control is stale with respect to any template edit, and regenerating it
// requires running the package's own build tooling. That tooling runs inside a
// container this provider constructs — a developer never runs it by hand — and
// the lock then proceeds over the freshly materialized control.
type debianKernelLock struct{}

func (debianKernelLock) Namespace() string { return "debian-kernel" }

func (debianKernelLock) ParamsHelp() string {
	return `gen-repo      APT repository for the gen-control container; default = repo
gen-dist      distribution for the gen-control container; default = dist
gen-arch      architecture for the gen-control container; default = arch

(all debian lockfile params are inherited and apply to the resolved lock)`
}

func (debianKernelLock) Generate(op OpContext, arg string) error {
	cfg, err := debianRepoConfig(op.Params)
	if err != nil {
		return err
	}
	pkgDir := filepath.Join(op.OutputDir, "pkgs", arg)
	build, err := buildcfg.LoadBuildYML(pkgDir)
	if err != nil {
		return err
	}

	if err := regenControlInContainer(op, pkgDir, build.GenControlPkgs, cfg); err != nil {
		return fmt.Errorf("regenerate debian/control: %w", err)
	}

	return runDebianPkgLock(op, arg, cfg)
}

// regenControlInContainer stands up an ephemeral chroot from the essential set
// plus the configured gen-control packages, copies the package's src/ tree into
// it, runs the package's control generator, and copies the regenerated
// debian/control back into pkgDir/src/debian/.
//
// The gen-control package set (gen_control_pkgs) is the minimal tooling tier the
// generator needs — the packaging's hand-authored Build-Depends bootstrap — not
// the full generated build-dependency set, which is what the lock computes
// afterwards from the regenerated control.
func regenControlInContainer(op OpContext, pkgDir string, genControlPkgs []string, cfg debianRepoCfg) error {
	l := log.From(op.Ctx, log.Lockfile)

	// The copy into the container runs inside the mount namespace, whose cwd is
	// not the launching process's; resolve pkgDir to an absolute path so the host
	// source tree is named the same way it is visible in the namespace.
	if abs, err := filepath.Abs(pkgDir); err == nil {
		pkgDir = abs
	}

	idx, err := lockfile.FetchBinaryIndex(op.Ctx, op.Store, cfg.GenRepo, cfg.GenDist, cfg.GenArch, cfg.Cookie)
	if err != nil {
		return err
	}

	var roots []string
	for _, p := range idx.EssentialPackages() {
		roots = append(roots, p.Name)
	}
	roots = append(roots, genControlPkgs...)

	pkgs, err := install.Resolve(idx, cfg.GenArch, roots)
	if err != nil {
		return err
	}
	l.Info("resolved %d packages for the control-generation container", len(pkgs))
	if err := lockfile.FetchDebs(op.Ctx, op.Store, cfg.GenRepo, pkgs); err != nil {
		return err
	}

	stubPath := op.StubPath
	if stubPath == "" {
		stubPath = container.StubPath()
	}

	stack, err := container.NewStack(container.StackConfig{Ctx: op.Ctx, StubPath: stubPath})
	if err != nil {
		return err
	}
	defer stack.Close()

	cont, rootfsPath, cleanup, err := install.BootstrapResolved(op.Ctx, stack.MountNS, op.Store, pkgs, stubPath)
	if err != nil {
		return fmt.Errorf("bootstrap control-generation container: %w", err)
	}
	defer cleanup()

	// Copy the source tree into the container's rootfs at /build. A copy, not a
	// read-only bind, because the generator writes into debian/ in place.
	// This shells out to `cp -a`: a faithful recursive copy must preserve modes,
	// timestamps, symlinks, and ownership, which the namespace's file interface
	// (Open/Mkdir/CreateFile/Symlink, no chmod/chown/utimes) cannot express. The
	// rootfs tmpfs is visible in the mount namespace at rootfsPath, so the copy
	// runs there and propagates into the container.
	buildDir := rootfsPath + "/build"
	if err := stack.MountNS.Mkdir(buildDir, 0o755); err != nil {
		return fmt.Errorf("mkdir /build: %w", err)
	}
	if err := container.Run(stack.MountNS, &container.ExecRequest{
		Argv: []string{"cp", "-a", filepath.Join(pkgDir, "src") + "/.", buildDir},
		Cwd:  "/",
	}); err != nil {
		return fmt.Errorf("copy source into container: %w", err)
	}

	// Generate debian/control inside the container. Cwd selects the directory —
	// no shell, no cd. gencontrol.py is the generator the packaging's
	// debian/rules invokes; PYTHONHASHSEED=0 makes its pickled config
	// reproducible. It writes debian/control (plus rules.gen and per-package
	// maintainer files); the lock reads only debian/control, which is what we
	// copy back out. Its output is routed through the project's log writers.
	stdout, stderr, closeFn := log.NewExecWriters(op.Ctx, log.Exec)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		closeFn()
		return err
	}
	err = container.Run(cont, &container.ExecRequest{
		Argv: []string{"debian/bin/gencontrol.py"},
		Env:  []string{"PYTHONHASHSEED=0"},
		Cwd:  "/build",
		FDs:  []*os.File{devNull, stdout, stderr},
	})
	devNull.Close()
	stdout.Close()
	stderr.Close()
	closeFn()
	if err != nil {
		return fmt.Errorf("generate debian/control: %w", err)
	}

	// Read the regenerated control back out through the mount namespace — the
	// same rootfs the container wrote it into — and write it to the working tree.
	return copyFileOut(stack.MountNS, buildDir+"/debian/control",
		filepath.Join(pkgDir, "src", "debian", "control"))
}

// copyFileOut reads a file from the mount namespace's rootfs and writes it to a
// host path, so a regenerated artifact lands back in the working tree. The
// namespace hands back a real file descriptor over its IPC channel, so no
// in-container process is involved.
func copyFileOut(mountNS *container.MountNS, nsPath, hostPath string) error {
	src, err := mountNS.Open(nsPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s in namespace: %w", nsPath, err)
	}
	defer src.Close()

	dst, err := os.Create(hostPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("copy %s: %w", nsPath, err)
	}
	return dst.Close()
}
