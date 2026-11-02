package provider

import (
	"fmt"
	"strings"
)

// Params is the opaque, provider-specific option set parsed from --params. Keys
// are provider-defined; a provider validates the set it receives and errors on
// an unknown or malformed key.
type Params map[string]string

// ParseParams parses one or more "key=val,key=val" fragments into a Params. A
// later assignment to the same key overrides an earlier one.
func ParseParams(fragments []string) (Params, error) {
	p := Params{}
	for _, frag := range fragments {
		if frag == "" {
			continue
		}
		for _, kv := range strings.Split(frag, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("malformed param %q (want key=val)", kv)
			}
			p[k] = v
		}
	}
	return p, nil
}
