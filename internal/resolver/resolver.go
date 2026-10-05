// Package resolver implements a backtracking constraint solver for Debian
// package dependency resolution. Given a package index and a set of root
// requirements, it computes a closed install set that satisfies all
// dependencies, pre-dependencies, and conflict constraints.
package resolver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gardenlinux/glbx/internal/debian/depends"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/debian/version"
)

// Requirement represents a single root requirement for the resolver.
type Requirement struct {
	Name            string // package name (or virtual package name)
	VersionOp       string // "", ">=", ">>", "=", "<=", "<<"
	Version         string // version string for constraint
	VirtualEligible bool   // if true, can be satisfied by Provides
}

// Result holds the resolved install set.
type Result struct {
	Packages []*index.Package // the fully closed, conflict-free install set
}

// Resolver is a backtracking constraint solver that computes a closed
// install set from a package index and a set of root requirements.
type Resolver struct {
	idx  *index.Index
	arch string // build architecture (e.g., "amd64")

	// Pre-expanded relations for each package.
	expandedDepends    map[string][]depGroup
	expandedPreDepends map[string][]depGroup
	expandedConflicts  map[string][]conflictEntry
	providedBy         map[string][]string // virtual name -> concrete package names
}

// depGroup represents an expanded dependency group (one OR-clause).
type depGroup struct {
	atoms      depends.Alternative // the original dependency atoms
	candidates []string            // concrete package names that can satisfy it
}

// conflictEntry records one atom from a Conflicts field with its expansion.
type conflictEntry struct {
	atom       depends.Dependency
	candidates []string // packages that the conflict targets
}

// New creates a new Resolver backed by the given package index.
// The arch parameter specifies the build architecture (e.g., "amd64").
// Dependency atoms qualified with a foreign architecture (pkg:foreignarch)
// are ignored during expansion.
func New(idx *index.Index, arch string) *Resolver {
	r := &Resolver{
		idx:                idx,
		arch:               arch,
		expandedDepends:    make(map[string][]depGroup),
		expandedPreDepends: make(map[string][]depGroup),
		expandedConflicts:  make(map[string][]conflictEntry),
		providedBy:         make(map[string][]string),
	}
	r.buildProvidedBy()
	r.expandRelations()
	return r
}

// buildProvidedBy constructs a lookup from virtual package names to the
// concrete packages that provide them.
func (r *Resolver) buildProvidedBy() {
	for _, pkg := range r.idx.All() {
		for _, alt := range pkg.Provides {
			for _, dep := range alt {
				if dep.Name != "" {
					r.providedBy[dep.Name] = append(r.providedBy[dep.Name], pkg.Name)
				}
			}
		}
	}
	// Sort for deterministic behavior.
	for k := range r.providedBy {
		sort.Strings(r.providedBy[k])
	}
}

// expandRelations pre-expands all dependency, pre-dependency, and conflict
// fields for every package in the index.
func (r *Resolver) expandRelations() {
	for _, pkg := range r.idx.All() {
		r.expandedDepends[pkg.Name] = r.expandDependencyList(pkg.Depends)
		r.expandedPreDepends[pkg.Name] = r.expandDependencyList(pkg.PreDepends)
		r.expandedConflicts[pkg.Name] = r.expandConflictList(pkg.Conflicts, pkg.Name)
	}
}

// expandDependencyList expands a DependencyList into depGroups with candidates.
func (r *Resolver) expandDependencyList(dl depends.DependencyList) []depGroup {
	var groups []depGroup
	for _, alt := range dl {
		g := depGroup{
			atoms:      alt,
			candidates: r.expandGroup(alt),
		}
		groups = append(groups, g)
	}
	return groups
}

// expandConflictList expands a Conflicts DependencyList into individual conflict entries.
func (r *Resolver) expandConflictList(dl depends.DependencyList, selfName string) []conflictEntry {
	var entries []conflictEntry
	for _, alt := range dl {
		for _, atom := range alt {
			candidates := r.expandAtom(atom)
			// Remove self from conflict targets.
			var filtered []string
			for _, c := range candidates {
				if c != selfName {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) > 0 {
				entries = append(entries, conflictEntry{atom: atom, candidates: filtered})
			}
		}
	}
	return entries
}

// expandGroup finds all concrete packages that satisfy any atom in an
// alternatives group (OR-clause).
func (r *Resolver) expandGroup(alt depends.Alternative) []string {
	seen := make(map[string]bool)
	var result []string
	for _, atom := range alt {
		for _, name := range r.expandAtom(atom) {
			if !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	sort.Strings(result)
	return result
}

// expandAtom finds all concrete packages that match a single dependency atom.
// This checks both the direct name and Provides declarations.
// Atoms with a foreign architecture qualifier (e.g., pkg:i386 on amd64) are
// skipped — they refer to multiarch foreign packages not in our index.
func (r *Resolver) expandAtom(atom depends.Dependency) []string {
	if atom.Arch != "" && atom.Arch != "any" && atom.Arch != "native" && atom.Arch != r.arch {
		return nil
	}

	var candidates []string
	seen := make(map[string]bool)

	// Check direct name match.
	if pkg := r.idx.Get(atom.Name); pkg != nil {
		if r.atomMatchesPkg(pkg, atom) {
			candidates = append(candidates, pkg.Name)
			seen[pkg.Name] = true
		}
	}

	// Check providers (virtual packages).
	for _, providerName := range r.providedBy[atom.Name] {
		if seen[providerName] {
			continue
		}
		pkg := r.idx.Get(providerName)
		if pkg == nil {
			continue
		}
		if r.atomMatchesPkg(pkg, atom) {
			candidates = append(candidates, providerName)
			seen[providerName] = true
		}
	}

	sort.Strings(candidates)
	return candidates
}

// atomMatchesPkg checks whether a package satisfies a dependency atom.
// It checks both the direct name and provides.
func (r *Resolver) atomMatchesPkg(pkg *index.Package, atom depends.Dependency) bool {
	// Direct match: package name matches and version constraint satisfied.
	if pkg.Name == atom.Name {
		if atom.Version == nil {
			return true
		}
		ok, _ := version.CheckConstraint(pkg.Version, atom.Version.Op, atom.Version.Version)
		return ok
	}

	// Check via Provides.
	for _, provAlt := range pkg.Provides {
		for _, prov := range provAlt {
			if prov.Name != atom.Name {
				continue
			}
			// If no version constraint on the dependency, any provide matches.
			if atom.Version == nil {
				return true
			}
			// If the provide has a version, check it against the constraint.
			if prov.Version != nil {
				ok, _ := version.CheckConstraint(prov.Version.Version, atom.Version.Op, atom.Version.Version)
				if ok {
					return true
				}
			}
			// Provide without a version cannot satisfy a versioned dependency.
		}
	}
	return false
}

// Resolve computes a closed install set satisfying all root requirements.
// Returns an error (of type *ResolutionError) if no solution exists.
func (r *Resolver) Resolve(roots []Requirement) (*Result, error) {
	state := newSolverState(r)

	// Enqueue root requirements.
	for _, root := range roots {
		if root.VirtualEligible {
			// Virtual-eligible: find candidates via direct or Provides.
			candidates := r.findCandidatesForRequirement(root)
			if len(candidates) == 0 {
				return nil, &ResolutionError{
					Attempts: []Attempt{{
						Package: root.Name,
						Reason:  fmt.Sprintf("package %q not found in index and has no providers", root.Name),
						Chain:   []string{fmt.Sprintf("requested: %s", root.Name)},
					}},
				}
			}
			state.enqueueVirtual(root.Name, candidates, []string{fmt.Sprintf("requested: %s", root.Name)})
		} else {
			// Direct: exact name must exist.
			pkg := r.idx.Get(root.Name)
			if pkg == nil {
				return nil, &ResolutionError{
					Attempts: []Attempt{{
						Package: root.Name,
						Reason:  fmt.Sprintf("package %q not found in index", root.Name),
						Chain:   []string{fmt.Sprintf("requested: %s", root.Name)},
					}},
				}
			}
			// Check version constraint if present.
			if root.VersionOp != "" {
				ok, _ := version.CheckConstraint(pkg.Version, root.VersionOp, root.Version)
				if !ok {
					return nil, &ResolutionError{
						Attempts: []Attempt{{
							Package: root.Name,
							Reason:  fmt.Sprintf("package %s has version %s but needs %s %s", root.Name, pkg.Version, root.VersionOp, root.Version),
							Chain:   []string{fmt.Sprintf("requested: %s", root.Name)},
						}},
					}
				}
			}
			state.enqueue(root.Name, []string{fmt.Sprintf("requested: %s", root.Name)})
		}
	}

	// Run the solver.
	if err := state.propagate(); err != nil {
		return nil, err
	}

	// Collect results.
	result := &Result{}
	for name := range state.selected {
		pkg := r.idx.Get(name)
		if pkg != nil {
			result.Packages = append(result.Packages, pkg)
		}
	}

	// Sort for deterministic output.
	sort.Slice(result.Packages, func(i, j int) bool {
		return result.Packages[i].Name < result.Packages[j].Name
	})

	return result, nil
}

// findCandidatesForRequirement finds all packages that can satisfy a requirement,
// considering both direct name and Provides.
func (r *Resolver) findCandidatesForRequirement(req Requirement) []string {
	atom := depends.Dependency{
		Name: req.Name,
	}
	if req.VersionOp != "" {
		atom.Version = &depends.VersionConstraint{
			Op:      req.VersionOp,
			Version: req.Version,
		}
	}
	return r.expandAtom(atom)
}

// rankCandidates orders candidates, preferring: already selected > direct name match > fewer deps.
func (r *Resolver) rankCandidates(candidates []string, atoms depends.Alternative, selected map[string][]string) []string {
	ranked := make([]string, len(candidates))
	copy(ranked, candidates)

	directNames := make(map[string]bool)
	for _, atom := range atoms {
		directNames[atom.Name] = true
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		ci, cj := ranked[i], ranked[j]

		// Prefer already-selected packages (no new addition needed).
		_, selI := selected[ci]
		_, selJ := selected[cj]
		if selI != selJ {
			return selI
		}

		// Prefer direct name match.
		dI := directNames[ci]
		dJ := directNames[cj]
		if dI != dJ {
			return dI
		}

		// Prefer packages with fewer dependencies (heuristic for minimal solutions).
		depsI := len(r.expandedDepends[ci]) + len(r.expandedPreDepends[ci])
		depsJ := len(r.expandedDepends[cj]) + len(r.expandedPreDepends[cj])
		if depsI != depsJ {
			return depsI < depsJ
		}

		// Tie-break alphabetically for determinism.
		return ci < cj
	})

	return ranked
}

// explainNoCandidates generates a human-readable explanation for why a
// dependency group has no viable candidates.
func (r *Resolver) explainNoCandidates(atoms depends.Alternative) string {
	var reasons []string
	for _, atom := range atoms {
		name := atom.Name
		if pkg := r.idx.Get(name); pkg != nil {
			if atom.Version != nil {
				reasons = append(reasons, fmt.Sprintf("%s has version %s but needs %s %s",
					name, pkg.Version, atom.Version.Op, atom.Version.Version))
			} else {
				reasons = append(reasons, fmt.Sprintf("%s exists but does not match", name))
			}
		} else if providers := r.providedBy[name]; len(providers) > 0 {
			reasons = append(reasons, fmt.Sprintf("%s is virtual, providers [%s] don't satisfy constraint",
				name, strings.Join(providers, ", ")))
		} else {
			reasons = append(reasons, fmt.Sprintf("%s not in index", name))
		}
	}
	if len(reasons) == 0 {
		return "no candidates"
	}
	return strings.Join(reasons, "; ")
}

// --- Solver state ---

// workItem represents a pending item in the work queue.
type workItem struct {
	pkg     string   // concrete package name to select (empty if virtual choice)
	virtual string   // virtual name (non-empty if this is a virtual choice)
	cands   []string // candidates for virtual choice
	chain   []string // dependency chain that led here
}

// decision records a decision point for backtracking.
type decision struct {
	depender         string
	atoms            depends.Alternative
	candidates       []string
	chosenIdx        int
	trailLen         int
	excludedSnapshot map[string]string
	queueSnapshot    []workItem
	branchFailures   []branchFailure
	chain            []string
}

// branchFailure records why a particular candidate at a decision point failed.
type branchFailure struct {
	candidate string
	message   string
}

// solverState holds the mutable state of the backtracking solver.
type solverState struct {
	resolver  *Resolver
	trail     []string            // order of selections
	selected  map[string][]string // package name -> reason chain
	excluded  map[string]string   // package name -> reason for exclusion
	decisions []*decision         // active decision stack
	exhausted []*decision         // fully-explored decisions (for error reporting)
	queue     []workItem          // pending work
}

func newSolverState(r *Resolver) *solverState {
	return &solverState{
		resolver: r,
		selected: make(map[string][]string),
		excluded: make(map[string]string),
	}
}

// enqueue adds a direct package requirement to the work queue.
func (s *solverState) enqueue(name string, chain []string) {
	s.queue = append(s.queue, workItem{pkg: name, chain: chain})
}

// enqueueVirtual adds a virtual package choice to the work queue.
func (s *solverState) enqueueVirtual(name string, candidates []string, chain []string) {
	s.queue = append(s.queue, workItem{virtual: name, cands: candidates, chain: chain})
}

// propagate processes the work queue, selecting packages and enforcing constraints.
// Returns nil on success, *ResolutionError on failure.
func (s *solverState) propagate() error {
	for {
		err := s.propagateInner()
		if err == nil {
			return nil
		}
		if !s.backtrack(err) {
			return s.buildError(err)
		}
	}
}

// conflict represents an internal resolution conflict that triggers backtracking.
type conflict struct {
	pkg     string
	chain   []string
	message string
}

func (c *conflict) Error() string { return c.message }

// propagateInner processes the queue until empty or a conflict occurs.
func (s *solverState) propagateInner() *conflict {
	for len(s.queue) > 0 {
		item := s.queue[0]
		s.queue = s.queue[1:]

		if item.virtual != "" {
			if err := s.handleVirtualChoice(item); err != nil {
				return err
			}
			continue
		}

		pkg := item.pkg
		chain := item.chain

		// Already selected? Nothing to do.
		if _, ok := s.selected[pkg]; ok {
			continue
		}

		// Excluded? This is a conflict.
		if reason, ok := s.excluded[pkg]; ok {
			return &conflict{
				pkg:     pkg,
				chain:   chain,
				message: fmt.Sprintf("%s is excluded: %s", pkg, reason),
			}
		}

		// Select this package.
		s.selected[pkg] = chain
		s.trail = append(s.trail, pkg)

		// Process conflicts: exclude all packages this one conflicts with.
		for _, ce := range s.resolver.expandedConflicts[pkg] {
			for _, other := range ce.candidates {
				if other == pkg {
					continue
				}
				if _, ok := s.selected[other]; ok {
					return &conflict{
						pkg:   pkg,
						chain: chain,
						message: fmt.Sprintf("%s conflicts with %s which is already selected",
							pkg, other),
					}
				}
				if _, ok := s.excluded[other]; !ok {
					s.excluded[other] = fmt.Sprintf("conflicts with %s", pkg)
				}
			}
		}

		// Process dependencies and pre-dependencies.
		if err := s.processDepGroups(pkg, chain, s.resolver.expandedPreDepends[pkg]); err != nil {
			return err
		}
		if err := s.processDepGroups(pkg, chain, s.resolver.expandedDepends[pkg]); err != nil {
			return err
		}
	}
	return nil
}

// handleVirtualChoice processes a virtual package choice from the queue.
func (s *solverState) handleVirtualChoice(item workItem) *conflict {
	// Filter out excluded candidates.
	var viable []string
	for _, c := range item.cands {
		if _, ok := s.excluded[c]; !ok {
			viable = append(viable, c)
		}
	}
	if len(viable) == 0 {
		return &conflict{
			pkg:     item.virtual,
			chain:   item.chain,
			message: fmt.Sprintf("all providers of %s are excluded", item.virtual),
		}
	}

	// If already selected, nothing to do.
	for _, c := range viable {
		if _, ok := s.selected[c]; ok {
			return nil
		}
	}

	if len(viable) == 1 {
		s.enqueue(viable[0], item.chain)
		return nil
	}

	// Multiple choices: make a decision.
	atoms := depends.Alternative{{Name: item.virtual}}
	ranked := s.resolver.rankCandidates(viable, atoms, s.selected)
	s.makeDecision(item.virtual, atoms, ranked, item.chain)
	return nil
}

// processDepGroups processes dependency groups for a selected package.
func (s *solverState) processDepGroups(pkg string, parentChain []string, groups []depGroup) *conflict {
	for _, g := range groups {
		chain := append(append([]string{}, parentChain...), fmt.Sprintf("%s depends on %s", pkg, formatAtoms(g.atoms)))

		candidates := g.candidates

		if len(candidates) == 0 {
			detail := s.resolver.explainNoCandidates(g.atoms)
			return &conflict{
				pkg:     pkg,
				chain:   chain,
				message: fmt.Sprintf("%s depends on %s: %s", pkg, formatAtoms(g.atoms), detail),
			}
		}

		// Filter out excluded candidates.
		var viable []string
		for _, c := range candidates {
			if _, ok := s.excluded[c]; !ok {
				viable = append(viable, c)
			}
		}

		if len(viable) == 0 {
			return &conflict{
				pkg:     pkg,
				chain:   chain,
				message: fmt.Sprintf("%s depends on %s, all candidates excluded", pkg, formatAtoms(g.atoms)),
			}
		}

		// Check if already satisfied by a selected package.
		alreadySatisfied := false
		for _, c := range viable {
			if _, ok := s.selected[c]; ok {
				alreadySatisfied = true
				break
			}
		}
		if alreadySatisfied {
			continue
		}

		if len(viable) == 1 {
			s.enqueue(viable[0], chain)
			continue
		}

		// Multiple choices: make a decision.
		ranked := s.resolver.rankCandidates(viable, g.atoms, s.selected)
		s.makeDecision(pkg, g.atoms, ranked, chain)
	}
	return nil
}

// makeDecision creates a decision point and enqueues the first candidate.
func (s *solverState) makeDecision(depender string, atoms depends.Alternative, candidates []string, chain []string) {
	d := &decision{
		depender:         depender,
		atoms:            atoms,
		candidates:       candidates,
		chosenIdx:        0,
		trailLen:         len(s.trail),
		excludedSnapshot: copyMap(s.excluded),
		queueSnapshot:    copyQueue(s.queue),
		chain:            chain,
	}
	s.decisions = append(s.decisions, d)
	s.enqueue(candidates[0], chain)
}

// backtrack undoes the most recent decision and tries the next candidate.
// Returns true if backtracking was possible, false if all options are exhausted.
func (s *solverState) backtrack(c *conflict) bool {
	for len(s.decisions) > 0 {
		d := s.decisions[len(s.decisions)-1]
		d.branchFailures = append(d.branchFailures, branchFailure{
			candidate: d.candidates[d.chosenIdx],
			message:   c.message,
		})

		nextIdx := d.chosenIdx + 1
		if nextIdx < len(d.candidates) {
			d.chosenIdx = nextIdx
			s.restore(d)
			s.enqueue(d.candidates[nextIdx], d.chain)
			return true
		}

		// This decision is fully exhausted.
		s.exhausted = append(s.exhausted, s.decisions[len(s.decisions)-1])
		s.decisions = s.decisions[:len(s.decisions)-1]
	}
	return false
}

// restore reverts solver state to a decision point's snapshot.
func (s *solverState) restore(d *decision) {
	// Undo selections made after this decision.
	for _, name := range s.trail[d.trailLen:] {
		delete(s.selected, name)
	}
	s.trail = s.trail[:d.trailLen]
	s.excluded = copyMap(d.excludedSnapshot)
	s.queue = copyQueue(d.queueSnapshot)
}

// buildError constructs a structured ResolutionError from the solver's
// exhausted decisions and final conflict.
func (s *solverState) buildError(finalConflict *conflict) *ResolutionError {
	re := &ResolutionError{}

	allDecisions := append(s.exhausted, s.decisions...)
	for _, d := range allDecisions {
		if len(d.branchFailures) == 0 {
			continue
		}
		attempt := Attempt{
			Package: d.depender,
			Reason:  fmt.Sprintf("depends on %s", formatAtoms(d.atoms)),
			Chain:   d.chain,
		}
		for _, bf := range d.branchFailures {
			attempt.SubErrors = append(attempt.SubErrors, Attempt{
				Package: bf.candidate,
				Reason:  bf.message,
				Chain:   d.chain,
			})
		}
		re.Attempts = append(re.Attempts, attempt)
	}

	// If no decision-based errors, add the direct conflict.
	if len(re.Attempts) == 0 && finalConflict != nil {
		re.Attempts = append(re.Attempts, Attempt{
			Package: finalConflict.pkg,
			Reason:  finalConflict.message,
			Chain:   finalConflict.chain,
		})
	}

	return re
}

// --- Utility functions ---

func formatAtoms(alt depends.Alternative) string {
	var parts []string
	for _, atom := range alt {
		s := atom.Name
		if atom.Version != nil {
			s += fmt.Sprintf(" (%s %s)", atom.Version.Op, atom.Version.Version)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

func copyMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func copyQueue(q []workItem) []workItem {
	c := make([]workItem, len(q))
	copy(c, q)
	return c
}
