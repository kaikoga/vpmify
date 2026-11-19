package recipe

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaikoga/vpmify/internal/semver"
)

// TagVersion is how a Git source reads the version of a tag from its name,
// for a repository whose tags are not SemVer (Warudo-Mod-Tool tags
// 0.15.2.2). The rule is the recipe's, not Vpmify's, so that no repository
// needs its own code in Vpmify (kaikoga 2026-11-19).
//
// Match is a regular expression on the whole tag name; a tag it does not
// match is not read. Version builds the version from the groups: {N} is
// group N, and {N:0W} is group N padded with zeros to W digits, so that
// "{1}.{2}.{3}{4:03}" reads 0.15.2.2 as 0.15.2002. A group that took no part
// in the match, or matched nothing, is 0. Leading zeros of the numbers in
// the version proper and its prerelease are dropped, which SemVer forbids
// there; build metadata keeps them.
//
// A tag the rule matches but cannot read is not read, as a tag that is not
// SemVer is not read without a rule: a version that does not build as
// SemVer, or a group longer than its padding, which would read out of order.
// Whoever writes a recipe may not own the repository, and its users cannot
// fix its tags, so a stray tag must not stop the recipe (kaikoga
// 2026-11-19).
type TagVersion struct {
	Match   string `json:"match"`
	Version string `json:"version"`
}

type tagVersionRule struct {
	match *regexp.Regexp
	parts []templatePart
}

// templatePart is literal text, or a group when group >= 0.
type templatePart struct {
	text  string
	group int
	width int
}

var placeholderBody = regexp.MustCompile(`^(\d+)(?::0(\d+))?$`)

// maxWidth is the widest padding: the digits of a version number, which
// is a uint64.
const maxWidth = 18

func parseTagVersion(tv *TagVersion) (*tagVersionRule, error) {
	if tv.Match == "" || tv.Version == "" {
		return nil, fmt.Errorf("needs match and version")
	}
	re, err := regexp.Compile(`^(?:` + tv.Match + `)$`)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	var parts []templatePart
	rest := tv.Version
	for rest != "" {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			parts = append(parts, templatePart{text: rest, group: -1})
			break
		}
		if rest[open] == '}' {
			return nil, fmt.Errorf("version: '}' without '{' in %q", tv.Version)
		}
		if open > 0 {
			parts = append(parts, templatePart{text: rest[:open], group: -1})
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return nil, fmt.Errorf("version: '{' without '}' in %q", tv.Version)
		}
		body := rest[open+1 : open+end]
		m := placeholderBody.FindStringSubmatch(body)
		if m == nil {
			return nil, fmt.Errorf("version: {%s} is not {N} or {N:0W}", body)
		}
		group, _ := strconv.Atoi(m[1])
		if group > re.NumSubexp() {
			return nil, fmt.Errorf("version: {%s} names group %d, but match has %d", body, group, re.NumSubexp())
		}
		width := 0
		if m[2] != "" {
			var err error
			if width, err = strconv.Atoi(m[2]); err != nil || width > maxWidth {
				return nil, fmt.Errorf("version: {%s} pads to at most %d digits", body, maxWidth)
			}
		}
		parts = append(parts, templatePart{group: group, width: width})
		rest = rest[open+end+1:]
	}
	return &tagVersionRule{match: re, parts: parts}, nil
}

// read returns the version of tag name, and false when the rule does not
// read it.
func (r *tagVersionRule) read(name string) (semver.Version, bool) {
	idx := r.match.FindStringSubmatchIndex(name)
	if idx == nil {
		return semver.Version{}, false
	}
	var b strings.Builder
	for _, p := range r.parts {
		if p.group < 0 {
			b.WriteString(p.text)
			continue
		}
		s := "0"
		if start, end := idx[2*p.group], idx[2*p.group+1]; start >= 0 && end > start {
			s = name[start:end]
		}
		if p.width > 0 && len(s) > p.width {
			// Unpadded, it would read out of order: 0.15.2.1000 after
			// 0.15.3.0 with {3}{4:03}.
			return semver.Version{}, false
		}
		for i := len(s); i < p.width; i++ {
			b.WriteByte('0')
		}
		b.WriteString(s)
	}
	v, err := semver.Parse(dropLeadingZeros(b.String()))
	if err != nil {
		return semver.Version{}, false
	}
	return v, true
}

// dropLeadingZeros drops the leading zeros of the numeric identifiers of
// the version proper and its prerelease, which SemVer forbids, and leaves
// build metadata, where it allows them.
func dropLeadingZeros(s string) string {
	build := ""
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s, build = s[:i], s[i:]
	}
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
	}
	out := trimIdentifiers(s)
	if pre != "" {
		out += "-" + trimIdentifiers(pre)
	}
	return out + build
}

func trimIdentifiers(s string) string {
	ids := strings.Split(s, ".")
	for i, id := range ids {
		if id == "" || strings.Trim(id, "0123456789") != "" {
			continue
		}
		if t := strings.TrimLeft(id, "0"); t != "" {
			ids[i] = t
		} else {
			ids[i] = "0"
		}
	}
	return strings.Join(ids, ".")
}

// TagVersionOf returns the version of tag name, and false when the source
// does not read the tag. Without a tagVersion rule, the name is the version
// with a leading "v" dropped, and a name that is not SemVer is not read.
func (s *Source) TagVersionOf(name string) (semver.Version, bool) {
	if s.tagVersion != nil {
		return s.tagVersion.read(name)
	}
	v, err := semver.Parse(strings.TrimPrefix(strings.TrimPrefix(name, "v"), "V"))
	if err != nil {
		return semver.Version{}, false
	}
	return v, true
}

// HasTagVersionRule reports whether the source reads tags by a tagVersion
// rule.
func (s *Source) HasTagVersionRule() bool {
	return s.tagVersion != nil
}
