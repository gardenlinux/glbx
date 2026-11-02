package provider

import (
	"fmt"
	"strings"
)

// Target is a parsed command-line operation target: a provider namespace and
// its single positional argument. A bare "rootfs" parses to {Namespace:
// "rootfs", Arg: ""}; "pkg:glibc" parses to {Namespace: "pkg", Arg: "glibc"}.
type Target struct {
	Namespace string
	Arg       string
}

// ParseTarget splits "ns:arg" into its parts. A string with no colon is a
// namespace with an empty argument.
func ParseTarget(s string) (Target, error) {
	ns, arg, found := strings.Cut(s, ":")
	if ns == "" {
		return Target{}, fmt.Errorf("empty namespace in target %q", s)
	}
	if found && arg == "" {
		return Target{}, fmt.Errorf("target %q has a colon but no argument", s)
	}
	return Target{Namespace: ns, Arg: arg}, nil
}
