package recipe

import (
	"strings"
	"testing"
)

// warudoTags reads Warudo-Mod-Tool's tags: 0.15.2.2, and 0.14.3 before it
// had a fourth number.
const warudoMatch = `(\\d+)\\.(\\d+)\\.(\\d+)(?:\\.(\\d+))?`

func tagRecipe(version string) string {
	return `{"id": "pkg", "sources": [{` + gitSourceHead + `,
  "tagVersion": {"match": "` + warudoMatch + `", "version": "` + version + `"},
  "packages": [{"name": "com.example.pkg", "versionFromTag": true}]}]}`
}

func TestTagVersionOf(t *testing.T) {
	cases := []struct {
		template string
		tags     map[string]string // tag -> version, "" for not read
	}{
		{"{1}.{2}.{3}{4:03}", map[string]string{
			"0.15.2.2":  "0.15.2002",
			"0.14.4.15": "0.14.4015",
			"0.14.3":    "0.14.3000", // the missing group is 0
			"0.15.0.5":  "0.15.5",    // "0005": leading zeros dropped
			"v0.15.2.2": "",          // the match is on the whole name
			"Samples":   "",
		}},
		{"{2}.{3}.{4}", map[string]string{"0.15.2.2": "15.2.2", "0.14.3": "14.3.0"}},
		// Build metadata keeps what the tag wrote, leading zeros and all.
		{"{1}.{2}.{3}{4:03}+{1}.{2}.{3}.{4:02}", map[string]string{"0.15.2.2": "0.15.2002+0.15.2.02"}},
		{"{1}.{2}.{3}-{4:03}", map[string]string{"0.15.2.2": "0.15.2-2"}},
	}
	for _, c := range cases {
		s, _ := onlyPackage(t, tagRecipe(c.template))
		for tag, want := range c.tags {
			v, ok, err := s.TagVersionOf(tag)
			if err != nil {
				t.Errorf("%s %s: %v", c.template, tag, err)
				continue
			}
			got := ""
			if ok {
				got = v.String()
			}
			if got != want {
				t.Errorf("%s %s = %q, want %q", c.template, tag, got, want)
			}
		}
	}

	// A tag the rule matches but cannot make SemVer of is an error.
	s, _ := onlyPackage(t, tagRecipe("{1}.{2}"))
	if _, _, err := s.TagVersionOf("0.15.2.2"); err == nil || !strings.Contains(err.Error(), `reads as "0.15"`) {
		t.Errorf("two numbers: err = %v", err)
	}

	// Without a rule, a leading "v" is dropped and other names are not read.
	s, _ = onlyPackage(t, gitRecipe(""))
	for tag, want := range map[string]bool{"v1.2.3": true, "1.2.3": true, "0.15.2.2": false} {
		if _, ok, err := s.TagVersionOf(tag); ok != want || err != nil {
			t.Errorf("no rule %s: ok=%v err=%v", tag, ok, err)
		}
	}
}

func TestTagVersionRejects(t *testing.T) {
	cases := []struct{ name, recipe, want string }{
		{"group beyond the match", tagRecipe("{1}.{2}.{5}"), "names group 5"},
		{"not a placeholder", tagRecipe("{1}.{x}"), "{x} is not"},
		{"padding without zero", tagRecipe("{1}.{2}.{3:3}"), "{3:3} is not"},
		{"open brace", tagRecipe("{1}.{2"), "without '}'"},
		{"close brace", tagRecipe("1}.2"), "without '{'"},
		{"bad match", `{"id": "pkg", "sources": [{` + gitSourceHead + `, "tagVersion": {"match": "(", "version": "1.0.0"}, "packages": [{"name": "p"}]}]}`, "match:"},
		{"no version", `{"id": "pkg", "sources": [{` + gitSourceHead + `, "tagVersion": {"match": ".*"}, "packages": [{"name": "p"}]}]}`, "needs match and version"},
		{"npm with tagVersion", `{"id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "tagVersion": {"match": ".*", "version": "1.0.0"}, "packages": [{"name": "p"}]}]}`, "takes no url, tagPattern, tagVersion"},
		{"npm with versionFromTag", `{"id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [{"name": "p", "versionFromTag": true}]}]}`, "versionFromTag"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.recipe))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestTagVersionChangesTheListingKey(t *testing.T) {
	base, _ := onlyPackage(t, gitRecipe(""))
	rule, _ := onlyPackage(t, `{"id": "pkg", "sources": [{`+gitSourceHead+`,
  "tagVersion": {"match": "`+warudoMatch+`", "version": "{1}.{2}.{3}{4:03}"},
  "packages": [{"name": "com.example.pkg"}]}]}`)
	fromTag, _ := onlyPackage(t, tagRecipe("{1}.{2}.{3}{4:03}"))
	keys := map[string]bool{base.ListingKey(): true, rule.ListingKey(): true, fromTag.ListingKey(): true}
	if len(keys) != 3 {
		t.Errorf("listing keys are not all different: %v", keys)
	}
}
