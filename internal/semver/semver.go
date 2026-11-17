// Package semver parses and compares Semantic Versioning 2.0.0 versions
// and evaluates the small range syntax used in recipes.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed SemVer 2.0.0 version.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
	raw                 string
}

// Parse parses a version such as "1.2.3", "1.2.3-beta.1" or "1.2.3+build".
func Parse(s string) (Version, error) {
	v := Version{raw: s}
	rest := s
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.Build = rest[i+1:]
		rest = rest[:i]
		if v.Build == "" {
			return Version{}, fmt.Errorf("invalid version %q: empty build metadata", s)
		}
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		pre := rest[i+1:]
		rest = rest[:i]
		if pre == "" {
			return Version{}, fmt.Errorf("invalid version %q: empty prerelease", s)
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" {
				return Version{}, fmt.Errorf("invalid version %q: empty prerelease identifier", s)
			}
		}
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version %q: want MAJOR.MINOR.PATCH", s)
	}
	nums := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("invalid version %q: bad number %q", s, p)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("invalid version %q: bad number %q", s, p)
		}
		*nums[i] = n
	}
	return v, nil
}

// String returns the version as it was written.
func (v Version) String() string { return v.raw }

// IsPrerelease reports whether the version has prerelease identifiers.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or 1. Build metadata is ignored, as SemVer requires.
func Compare(a, b Version) int {
	for _, d := range [3][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := comparePreID(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

func comparePreID(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

type comparator struct {
	op string
	v  Version
}

func (c comparator) matches(v Version) bool {
	d := Compare(v, c.v)
	switch c.op {
	case ">=":
		return d >= 0
	case ">":
		return d > 0
	case "<=":
		return d <= 0
	case "<":
		return d < 0
	default:
		return d == 0
	}
}

// Range is a set of comparators: whitespace means AND, "||" means OR.
// Supported operators are >=, >, <=, < and = (a bare version means =).
// The empty string and "*" match every version.
type Range struct {
	raw  string
	sets [][]comparator
}

// ParseRange parses a range such as ">=3.4.0", ">=1.0.0 <2.0.0" or "1.0.0 || >=2.0.0".
func ParseRange(s string) (Range, error) {
	r := Range{raw: s}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || trimmed == "*" {
		return r, nil
	}
	for _, alt := range strings.Split(trimmed, "||") {
		fields := strings.Fields(alt)
		if len(fields) == 0 {
			return Range{}, fmt.Errorf("invalid range %q: empty alternative", s)
		}
		var set []comparator
		for i := 0; i < len(fields); i++ {
			tok := fields[i]
			op := ""
			for _, candidate := range []string{">=", "<=", ">", "<", "="} {
				if strings.HasPrefix(tok, candidate) {
					op = candidate
					tok = tok[len(candidate):]
					break
				}
			}
			if tok == "" {
				// Operator written apart from its version, as in ">= 1.0.0".
				i++
				if i >= len(fields) {
					return Range{}, fmt.Errorf("invalid range %q: operator without version", s)
				}
				tok = fields[i]
			}
			v, err := Parse(tok)
			if err != nil {
				return Range{}, fmt.Errorf("invalid range %q: %w", s, err)
			}
			set = append(set, comparator{op: op, v: v})
		}
		r.sets = append(r.sets, set)
	}
	return r, nil
}

// String returns the range as it was written.
func (r Range) String() string { return r.raw }

// Contains reports whether v satisfies the range.
func (r Range) Contains(v Version) bool {
	if len(r.sets) == 0 {
		return true
	}
	for _, set := range r.sets {
		ok := true
		for _, c := range set {
			if !c.matches(v) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
