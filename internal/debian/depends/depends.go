// Package depends implements a parser for Debian dependency expressions,
// as used in Depends, Build-Depends, and related control file fields.
package depends

import (
	"fmt"
	"strings"
	"unicode"
)

// VersionConstraint represents a version relation (e.g., ">= 2.0").
type VersionConstraint struct {
	Op      string // ">>", ">=", "=", "<=", "<<"
	Version string
}

// Dependency represents a single package dependency with optional qualifiers.
type Dependency struct {
	Name        string             // package name
	Arch        string             // arch qualifier after colon (e.g., "amd64" in pkg:amd64)
	Version     *VersionConstraint // optional version constraint
	ArchList    []string           // architecture restriction list [amd64 arm64] or [!i386]
	ArchExclude bool               // true if arch list is exclusion (items prefixed with !)
	Profiles    [][]string         // build profile groups <cross> <!nocheck>
}

// Alternative represents a set of alternative dependencies separated by |.
type Alternative []Dependency

// DependencyList represents a complete dependency specification (clauses separated by ,).
type DependencyList []Alternative

// Parse parses a Debian dependency string into a DependencyList.
// It handles the full syntax including version constraints, arch qualifiers,
// architecture restrictions, build profiles, and alternatives.
func Parse(input string) (DependencyList, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	var result DependencyList
	clauses := splitTopLevel(input, ',')

	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}

		alternatives := splitTopLevel(clause, '|')
		var alt Alternative

		for _, altStr := range alternatives {
			dep, err := parseSingleDep(strings.TrimSpace(altStr))
			if err != nil {
				return nil, err
			}
			alt = append(alt, dep)
		}

		if len(alt) > 0 {
			result = append(result, alt)
		}
	}

	return result, nil
}

// splitTopLevel splits a string by a delimiter, but only at the top level
// (not inside parentheses, brackets, or angle brackets).
func splitTopLevel(s string, delim byte) []string {
	var parts []string
	parenDepth := 0
	bracketDepth := 0
	angleDepth := 0
	start := 0

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			if bracketDepth == 0 && angleDepth == 0 {
				parenDepth++
			}
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case '[':
			if parenDepth == 0 && angleDepth == 0 {
				bracketDepth++
			}
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case '<':
			if parenDepth == 0 && bracketDepth == 0 {
				angleDepth++
			}
		case '>':
			if angleDepth > 0 && parenDepth == 0 && bracketDepth == 0 {
				angleDepth--
			}
		case delim:
			if parenDepth == 0 && bracketDepth == 0 && angleDepth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// parseSingleDep parses a single dependency atom (no | or , delimiters).
func parseSingleDep(s string) (Dependency, error) {
	if s == "" {
		return Dependency{}, fmt.Errorf("depends: empty dependency")
	}

	var dep Dependency
	p := &parser{input: s, pos: 0}

	// Parse package name (and optional arch qualifier)
	name := p.consumePackageName()
	if name == "" {
		return Dependency{}, fmt.Errorf("depends: expected package name in %q", s)
	}

	// Check for arch qualifier (colon followed by arch name)
	if p.peek() == ':' {
		p.pos++ // skip ':'
		arch := p.consumeToken()
		if arch == "" {
			return Dependency{}, fmt.Errorf("depends: expected architecture after colon in %q", s)
		}
		dep.Name = name
		dep.Arch = arch
	} else {
		dep.Name = name
	}

	p.skipWhitespace()

	// Parse optional version constraint: (op version)
	if p.peek() == '(' {
		vc, err := p.parseVersionConstraint()
		if err != nil {
			return Dependency{}, fmt.Errorf("depends: %v in %q", err, s)
		}
		dep.Version = vc
	}

	p.skipWhitespace()

	// Parse optional architecture restriction: [arch1 arch2] or [!arch1]
	if p.peek() == '[' {
		archList, exclude, err := p.parseArchRestriction()
		if err != nil {
			return Dependency{}, fmt.Errorf("depends: %v in %q", err, s)
		}
		dep.ArchList = archList
		dep.ArchExclude = exclude
	}

	p.skipWhitespace()

	// Parse optional build profiles: <profile1 profile2> <profile3>
	for p.peek() == '<' {
		profiles, err := p.parseProfileGroup()
		if err != nil {
			return Dependency{}, fmt.Errorf("depends: %v in %q", err, s)
		}
		dep.Profiles = append(dep.Profiles, profiles)
		p.skipWhitespace()
	}

	return dep, nil
}

// parser is a simple string position-based parser.
type parser struct {
	input string
	pos   int
}

func (p *parser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

// consumePackageName reads a Debian package name.
// Package names consist of lowercase alphanumerics, +, -, and . (must start with alnum).
func (p *parser) consumePackageName() string {
	start := p.pos
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '+' || c == '-' || c == '.' {
			p.pos++
		} else {
			break
		}
	}
	return p.input[start:p.pos]
}

// consumeToken reads a whitespace-delimited token of alphanumerics and common punctuation.
func (p *parser) consumeToken() string {
	start := p.pos
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if unicode.IsSpace(rune(c)) || c == ')' || c == ']' || c == '>' || c == '(' || c == '[' || c == '<' || c == ',' || c == '|' {
			break
		}
		p.pos++
	}
	return p.input[start:p.pos]
}

// parseVersionConstraint parses "(op version)".
func (p *parser) parseVersionConstraint() (*VersionConstraint, error) {
	if p.peek() != '(' {
		return nil, fmt.Errorf("expected '(' for version constraint")
	}
	p.pos++ // skip '('
	p.skipWhitespace()

	// Parse operator
	op := p.consumeOp()
	if op == "" {
		return nil, fmt.Errorf("expected version operator")
	}
	if !isValidOp(op) {
		return nil, fmt.Errorf("invalid version operator %q", op)
	}

	p.skipWhitespace()

	// Parse version string (everything up to ')')
	verStart := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != ')' {
		p.pos++
	}
	version := strings.TrimSpace(p.input[verStart:p.pos])
	if version == "" {
		return nil, fmt.Errorf("expected version string")
	}

	if p.pos >= len(p.input) || p.input[p.pos] != ')' {
		return nil, fmt.Errorf("expected ')' closing version constraint")
	}
	p.pos++ // skip ')'

	return &VersionConstraint{Op: op, Version: version}, nil
}

// consumeOp reads a version comparison operator.
func (p *parser) consumeOp() string {
	start := p.pos
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c == '>' || c == '<' || c == '=' {
			p.pos++
		} else {
			break
		}
	}
	return p.input[start:p.pos]
}

// isValidOp checks that op is one of the valid Debian version operators.
func isValidOp(op string) bool {
	switch op {
	case ">>", ">=", "=", "<=", "<<":
		return true
	}
	return false
}

// parseArchRestriction parses "[arch1 arch2 ...]" or "[!arch1 !arch2 ...]".
func (p *parser) parseArchRestriction() ([]string, bool, error) {
	if p.peek() != '[' {
		return nil, false, fmt.Errorf("expected '[' for arch restriction")
	}
	p.pos++ // skip '['
	p.skipWhitespace()

	var archs []string
	exclude := false
	first := true

	for p.pos < len(p.input) && p.input[p.pos] != ']' {
		p.skipWhitespace()
		if p.pos < len(p.input) && p.input[p.pos] == ']' {
			break
		}

		token := ""
		isExclude := false
		if p.pos < len(p.input) && p.input[p.pos] == '!' {
			isExclude = true
			p.pos++ // skip '!'
		}

		token = p.consumeArchToken()
		if token == "" {
			break
		}

		if first {
			exclude = isExclude
			first = false
		}

		archs = append(archs, token)
	}

	if p.pos >= len(p.input) || p.input[p.pos] != ']' {
		return nil, false, fmt.Errorf("expected ']' closing arch restriction")
	}
	p.pos++ // skip ']'

	return archs, exclude, nil
}

// consumeArchToken reads an architecture name token.
func (p *parser) consumeArchToken() string {
	start := p.pos
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' {
			p.pos++
		} else {
			break
		}
	}
	return p.input[start:p.pos]
}

// parseProfileGroup parses a single "<profile1 profile2>" group.
func (p *parser) parseProfileGroup() ([]string, error) {
	if p.peek() != '<' {
		return nil, fmt.Errorf("expected '<' for build profile")
	}
	p.pos++ // skip '<'
	p.skipWhitespace()

	var profiles []string
	for p.pos < len(p.input) && p.input[p.pos] != '>' {
		p.skipWhitespace()
		if p.pos < len(p.input) && p.input[p.pos] == '>' {
			break
		}

		token := p.consumeProfileToken()
		if token == "" {
			break
		}
		profiles = append(profiles, token)
	}

	if p.pos >= len(p.input) || p.input[p.pos] != '>' {
		return nil, fmt.Errorf("expected '>' closing build profile")
	}
	p.pos++ // skip '>'

	return profiles, nil
}

// consumeProfileToken reads a build profile token (may start with !).
func (p *parser) consumeProfileToken() string {
	start := p.pos
	// Allow leading '!' for negated profiles
	if p.pos < len(p.input) && p.input[p.pos] == '!' {
		p.pos++
	}
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' {
			p.pos++
		} else {
			break
		}
	}
	return p.input[start:p.pos]
}
