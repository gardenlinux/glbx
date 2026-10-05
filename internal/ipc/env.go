package ipc

import "strings"

// DefaultPATH is the PATH used as the sole entry in the base env when an
// ExecRequest sets ResetEnv. Mirrors the historical stub PATH; chosen to be
// usable inside both host gl and freshly-pivoted rootfs.
const DefaultPATH = "/usr/sbin:/usr/bin:/sbin:/bin"

// ResolveEnv computes the final env slice for a child process under the
// overlay-style ExecRequest semantics:
//
//   - reset == false → base = inherited
//   - reset == true  → base = []string{"PATH=" + DefaultPATH}
//   - overlay entries are merged onto base: keys present in base are
//     overridden in place (preserving original position); new keys are
//     appended in overlay order.
//
// Entries missing '=' are silently dropped.
func ResolveEnv(reset bool, overlay, inherited []string) []string {
	var base []string
	if reset {
		base = []string{"PATH=" + DefaultPATH}
	} else {
		base = inherited
	}
	if len(overlay) == 0 {
		return base
	}
	idx := make(map[string]int, len(base)+len(overlay))
	out := make([]string, 0, len(base)+len(overlay))
	add := func(kv string) {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return
		}
		k := kv[:eq]
		if i, ok := idx[k]; ok {
			out[i] = kv
			return
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		add(kv)
	}
	for _, kv := range overlay {
		add(kv)
	}
	return out
}
