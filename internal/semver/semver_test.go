package semver

import "testing"

func TestParseRejects(t *testing.T) {
	for _, s := range []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3+", "1.2.3-a..b", "v1.2.3", "1.x.3"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded", s)
		}
	}
}

func TestCompareOrder(t *testing.T) {
	// The precedence example from SemVer 2.0.0, item 11.
	order := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0",
	}
	for i := 0; i+1 < len(order); i++ {
		a, b := mustParse(t, order[i]), mustParse(t, order[i+1])
		if got := Compare(a, b); got != -1 {
			t.Errorf("Compare(%s, %s) = %d, want -1", order[i], order[i+1], got)
		}
		if got := Compare(b, a); got != 1 {
			t.Errorf("Compare(%s, %s) = %d, want 1", order[i+1], order[i], got)
		}
	}
	if got := Compare(mustParse(t, "1.0.0+a"), mustParse(t, "1.0.0+b")); got != 0 {
		t.Errorf("build metadata compared: %d", got)
	}
}

func TestRange(t *testing.T) {
	cases := []struct {
		rng  string
		v    string
		want bool
	}{
		{"", "0.0.1", true},
		{"*", "9.9.9", true},
		{">=3.4.0", "3.4.0", true},
		{">=3.4.0", "3.3.9", false},
		{">= 3.4.0", "3.5.0", true},
		{">=1.0.0 <2.0.0", "1.9.9", true},
		{">=1.0.0 <2.0.0", "2.0.0", false},
		{"1.0.0 || >=2.0.0", "1.0.0", true},
		{"1.0.0 || >=2.0.0", "1.5.0", false},
		{"1.0.0 || >=2.0.0", "2.1.0", true},
		{"=1.2.3", "1.2.3", true},
		{"<=1.2.3", "1.2.4", false},
		{">1.2.3", "1.2.3", false},
	}
	for _, c := range cases {
		r, err := ParseRange(c.rng)
		if err != nil {
			t.Fatalf("ParseRange(%q): %v", c.rng, err)
		}
		if got := r.Contains(mustParse(t, c.v)); got != c.want {
			t.Errorf("%q contains %s = %v, want %v", c.rng, c.v, got, c.want)
		}
	}
	for _, bad := range []string{">=", ">=x", "1.0.0 ||", "|| 1.0.0"} {
		if _, err := ParseRange(bad); err == nil {
			t.Errorf("ParseRange(%q) succeeded", bad)
		}
	}
}

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
