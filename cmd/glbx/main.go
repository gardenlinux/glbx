package main

import (
	"fmt"
	"os"

	"github.com/gardenlinux/glbx/internal/log"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "import":
		err = cmdImport(args)
	case "lockfile":
		err = cmdLockfile(args)
	case "lockfile-rootfs":
		err = cmdLockfileRootfs(args)
	case "build":
		err = cmdBuild(args)
	case "graph":
		err = cmdGraph(args)
	case "cache":
		err = cmdCache(args)
	case "resolve":
		err = cmdResolve(args)
	case "status":
		err = cmdStatus(args)
	case "exec-chroot":
		err = cmdExecChroot(args)
	case "help", "--help", "-h":
		usage()
		return
	default:
		_, l := rootContext(log.Engine)
		l.Error("unknown command %q", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		_, l := rootContext(log.Engine)
		l.Error("glbx %s: %v", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: glbx <command> [arguments]")
	fmt.Fprintln(os.Stderr, "commands: import, lockfile, lockfile-rootfs, build, graph, cache, resolve, status, exec-chroot")
}
