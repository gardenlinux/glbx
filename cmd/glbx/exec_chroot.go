package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func cmdExecChroot(args []string) error {
	fs := flag.NewFlagSet("exec-chroot", flag.ExitOnError)
	cacheDir := fs.String("cache", os.Getenv("GLBX_CACHE"), "cache directory")
	explore := fs.Bool("explore", false, "skip container creation, exec inside the mount namespace")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr,
			"usage: glbx exec-chroot [--cache dir] [--explore] <rootfs-hash> <cmd> [args...]")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fs.Usage()
		return fmt.Errorf("expected <rootfs-hash> and <cmd>")
	}
	rootfsHashStr := rest[0]
	cmdArgv := rest[1:]

	ctx, l := rootContext(log.Engine)

	// Open the object store.
	storeRoot := *cacheDir
	if storeRoot == "" {
		storeRoot = objstore.DefaultRoot()
	}
	store, err := objstore.Open(storeRoot)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}

	// Parse and validate the rootfs hash.
	rootfsHash, err := objstore.NewHash(rootfsHashStr)
	if err != nil {
		return fmt.Errorf("invalid rootfs hash: %w", err)
	}

	// Look up the rootfs hash in the map to get the manifest blob hash.
	manifestHash, err := store.Map.Get(rootfsHash)
	if err != nil {
		return fmt.Errorf("map lookup for %s: %w", rootfsHash, err)
	}

	// Read manifest to find the rootfs.tar.gz entry.
	manifestReader, err := store.Blobs.Open(manifestHash)
	if err != nil {
		return fmt.Errorf("open manifest %s: %w", manifestHash, err)
	}
	manifestData, err := io.ReadAll(manifestReader)
	manifestReader.Close()
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	var tarBlobHash objstore.Hash
	for _, line := range strings.Split(string(manifestData), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && parts[1] == "rootfs.tar.gz" {
			tarBlobHash, err = objstore.NewHash(parts[0])
			if err != nil {
				return fmt.Errorf("invalid tar blob hash in manifest: %w", err)
			}
			break
		}
	}
	if tarBlobHash == (objstore.Hash{}) {
		return fmt.Errorf("rootfs.tar.gz not found in manifest")
	}

	// Determine the exec_env_stub binary path.
	stubPath := os.Getenv("GLBX_STUB_PATH")
	if stubPath == "" {
		stubPath = container.StubPath()
	}

	stack, err := container.NewStack(container.StackConfig{
		Ctx:      ctx,
		StubPath: stubPath,
	})
	if err != nil {
		return err
	}
	defer stack.Close()
	mountNS := stack.MountNS

	// Mount tmpfs inside the MountNS for rootfs extraction.
	workPath, err := mountNS.MkTempDir("/tmp", "glbx-exec-chroot-")
	if err != nil {
		return fmt.Errorf("mktempdir: %w", err)
	}
	if err := mountNS.Mount("tmpfs", workPath, "tmpfs", 0, "size=2g"); err != nil {
		return fmt.Errorf("mount tmpfs on %s: %w", workPath, err)
	}

	rootfsPath := workPath + "/rootfs"
	if err := mountNS.Mkdir(rootfsPath, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", rootfsPath, err)
	}

	// Bind-mount the tar.gz blob into the MountNS.
	blobPath := store.Blobs.Path(tarBlobHash)
	tarMountPath := workPath + "/rootfs.tar.gz"
	if err := mountNS.CreateFile(tarMountPath, 0644); err != nil {
		return fmt.Errorf("create mount target: %w", err)
	}
	if err := mountNS.Mount(blobPath, tarMountPath, "", syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("bind-mount tar blob: %w", err)
	}

	// Extract rootfs tarball inside MountNS (onto tmpfs).
	l.Info("extracting rootfs (%s) into tmpfs", tarBlobHash.Short())
	if err := container.Run(mountNS, &container.ExecRequest{
		Argv: []string{"tar", "-xzf", tarMountPath, "-C", rootfsPath},
		Cwd:  "/",
	}); err != nil {
		return fmt.Errorf("extract rootfs tar: %w", err)
	}

	if *explore {
		l.Info("exploring rootfs (no container) at %s", rootfsPath)
		pid, err := mountNS.Exec(&container.ExecRequest{
			Argv: cmdArgv,
			Cwd:  rootfsPath,
			Env:  []string{"GLBX_ROOTFS=" + rootfsPath},
			FDs:  []*os.File{os.Stdin, os.Stdout, os.Stderr},
		})
		if err != nil {
			return fmt.Errorf("exec in mount namespace: %w", err)
		}
		exitCode, err := mountNS.Wait(pid)
		if err != nil {
			return fmt.Errorf("wait for process: %w", err)
		}
		if exitCode != 0 {
			os.Exit(exitCode)
		}
		return nil
	}

	// Create the container with the extracted rootfs on tmpfs.
	l.Info("creating container")
	ctr, err := container.NewContainer(container.ContainerConfig{
		Ctx:      ctx,
		Parent:   mountNS,
		StubPath: stubPath,
		Rootfs:   rootfsPath,
	})
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	defer ctr.Close()

	// Execute the command inside the container.
	containerEnv := []string{"HOME=/root"}
	if term := os.Getenv("TERM"); term != "" {
		containerEnv = append(containerEnv, "TERM="+term)
	}
	pid, err := ctr.Exec(&container.ExecRequest{
		Argv: cmdArgv,
		Cwd:  "/",
		Env:  containerEnv,
	})
	if err != nil {
		return fmt.Errorf("exec in container: %w", err)
	}

	// Wait for the command to finish and exit with the same code.
	exitCode, err := ctr.Wait(pid)
	if err != nil {
		return fmt.Errorf("wait for process: %w", err)
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}
