package ojson

import (
	"strings"
	"testing"
)

func TestKeepsOrderAndValues(t *testing.T) {
	in := `{"z": 1, "a": {"nested": [1, 2]}, "m": "<&>", "a2": null}`
	o, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(o.Keys(), ","), "z,a,m,a2"; got != want {
		t.Errorf("keys = %s, want %s", got, want)
	}
	out, err := o.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `{"z":1,"a":{"nested": [1, 2]},"m":"<&>","a2":null}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSetAndDelete(t *testing.T) {
	o, err := Parse([]byte(`{"a":1,"b":2,"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	o.Set("b", []byte(`"two"`))
	o.Delete("a")
	if err := o.SetValue("d", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	out, _ := o.MarshalJSON()
	if got, want := string(out), `{"b":"two","c":3,"d":["x"]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestFormat(t *testing.T) {
	o, err := Parse([]byte(`{"a":[],"b":{"c":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := o.Format("  ", "\r\n", true)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\r\n  \"a\": [],\r\n  \"b\": {\r\n    \"c\": 1\r\n  }\r\n}\r\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{`[]`, `{"a":1} {}`, `{"a":}`, ``} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("Parse(%q) succeeded", s)
		}
	}
}
