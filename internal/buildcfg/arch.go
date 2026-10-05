package buildcfg

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// HostArch returns the Debian architecture name for the host machine.
//
// The Debian architecture vocabulary (amd64, arm64, …) does not match Go's
// GOARCH in general (386 vs i386, arm vs armhf, ppc64le vs ppc64el). When
// available, dpkg --print-architecture is authoritative; otherwise GOARCH is
// mapped. The result is the default target architecture for a build; callers
// that accept --arch pass this as the default rather than hard-coding one, so
// the native architecture is never silently emulated.
func HostArch() string {
	hostArchOnce.Do(func() {
		hostArch = detectHostArch()
	})
	return hostArch
}

var (
	hostArchOnce sync.Once
	hostArch     string
)

func detectHostArch() string {
	if out, err := exec.Command("dpkg", "--print-architecture").Output(); err == nil {
		if a := strings.TrimSpace(string(out)); a != "" {
			return a
		}
	}
	return goArchToDebian(runtime.GOARCH)
}

func goArchToDebian(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "386":
		return "i386"
	case "arm":
		return "armhf"
	case "ppc64le":
		return "ppc64el"
	case "s390x":
		return "s390x"
	case "riscv64":
		return "riscv64"
	default:
		return goarch
	}
}
