package depends

import "strings"

// MatchesArch reports whether a dependency applies to the given architecture,
// based on the dependency's architecture restriction list.
func (dep *Dependency) MatchesArch(arch string) bool {
	if len(dep.ArchList) == 0 {
		return true
	}
	if dep.ArchExclude {
		for _, a := range dep.ArchList {
			if ArchMatches(a, arch) {
				return false
			}
		}
		return true
	}
	for _, a := range dep.ArchList {
		if ArchMatches(a, arch) {
			return true
		}
	}
	return false
}

// ExcludedByProfiles reports whether a dependency is excluded given the active
// build profiles. Per Debian policy: a dep with profile restrictions is included
// only when at least one restriction group is satisfied. A group is satisfied
// when ALL its terms match: "!foo" matches when foo is NOT active, "foo" matches
// when foo IS active.
func (dep *Dependency) ExcludedByProfiles(activeProfiles []string) bool {
	if len(dep.Profiles) == 0 {
		return false
	}
	profileSet := make(map[string]bool)
	for _, p := range activeProfiles {
		profileSet[p] = true
	}
	for _, group := range dep.Profiles {
		groupSatisfied := true
		for _, term := range group {
			if strings.HasPrefix(term, "!") {
				if profileSet[term[1:]] {
					groupSatisfied = false
					break
				}
			} else {
				if !profileSet[term] {
					groupSatisfied = false
					break
				}
			}
		}
		if groupSatisfied {
			return false
		}
	}
	return true
}

// ArchMatches checks if an architecture pattern matches a specific arch.
// Handles wildcards like "linux-any", "any-cpu", "any", and exact matches.
func ArchMatches(pattern, arch string) bool {
	if pattern == "any" {
		return true
	}
	if pattern == arch {
		return true
	}
	if strings.HasSuffix(pattern, "-any") {
		os := strings.TrimSuffix(pattern, "-any")
		if os == "linux" {
			return true
		}
		return false
	}
	if strings.HasPrefix(pattern, "any-") {
		return true
	}
	return false
}
