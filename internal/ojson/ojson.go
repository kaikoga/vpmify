// Package ojson reads and writes a JSON object while keeping the order of
// its keys, so that files owned by other tools (ALCOM's settings.json, an
// upstream package.json) change only where Vpmify means to change them.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Object is a JSON object whose values are kept as raw JSON.
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// New returns an empty object.
func New() *Object {
	return &Object{vals: map[string]json.RawMessage{}}
}

// Parse reads a JSON object. For a duplicated key, the last value wins and
// the key keeps its first position.
func Parse(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	o := New()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected token %v", tok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.Set(key, raw)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("trailing data after JSON object")
	}
	return o, nil
}

// Keys returns the keys in order.
func (o *Object) Keys() []string {
	return append([]string(nil), o.keys...)
}

// Get returns the raw value of key.
func (o *Object) Get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// GetInto decodes the value of key into dst. It reports whether key exists.
func (o *Object) GetInto(key string, dst any) (bool, error) {
	v, ok := o.vals[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(v, dst)
}

// Set replaces the value of key, or appends key when it is new.
func (o *Object) Set(key string, raw json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = append(json.RawMessage(nil), raw...)
}

// SetValue marshals v and sets it as the value of key.
func (o *Object) SetValue(key string, v any) error {
	raw, err := marshalNoEscape(v)
	if err != nil {
		return err
	}
	o.Set(key, raw)
	return nil
}

// Delete removes key.
func (o *Object) Delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// MarshalJSON writes the object compactly, keeping the key order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshalNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Format writes the object indented with indent, with eol as the line ending.
// A trailing line ending is added when trailingEOL is true.
func (o *Object) Format(indent, eol string, trailingEOL bool) ([]byte, error) {
	compact, err := o.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", indent); err != nil {
		return nil, err
	}
	// JSON strings cannot contain a raw newline, so every "\n" here is formatting.
	out := buf.String()
	if eol != "\n" {
		out = strings.ReplaceAll(out, "\n", eol)
	}
	if trailingEOL {
		out += eol
	}
	return []byte(out), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// DetectEOL returns "\r\n" when data uses CRLF line endings, otherwise "\n".
func DetectEOL(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// HasTrailingEOL reports whether data ends with a line ending.
func HasTrailingEOL(data []byte) bool {
	return bytes.HasSuffix(data, []byte("\n"))
}
