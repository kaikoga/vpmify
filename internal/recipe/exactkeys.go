package recipe

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/kaikoga/vpmify/internal/ojson"
)

// unmarshalExact is json.Unmarshal with keys matched to fields by their exact
// spelling. encoding/json also takes a key that differs from a field's name
// only in case ("Versions" for "versions"), and a reader that tells case
// apart, such as a Vpmify rewritten in Rust, would ignore it as a field it
// does not know: a recipe means the same to every reader only when the key is
// spelled as the format writes it (kaikoga 2026-11-21).
func unmarshalExact(data []byte, v any) error {
	if exact, err := exactKeys(data, reflect.TypeOf(v)); err == nil {
		data = exact
	}
	// On an error, json.Unmarshal reads the data as it was and says what is
	// wrong with it.
	return json.Unmarshal(data, v)
}

var (
	rawMessageType  = reflect.TypeFor[json.RawMessage]()
	unmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// exactKeys drops from data the keys of objects read into structs of t that
// do not spell a field's name exactly. Such keys are ignored as unknown, which
// those that differ only in case would not be. A type with its own
// UnmarshalJSON reads its value as it is, and matches keys itself.
func exactKeys(data []byte, t reflect.Type) ([]byte, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if !hasStructs(t, map[reflect.Type]bool{}) {
		return data, nil
	}
	trimmed := bytes.TrimSpace(data)
	switch t.Kind() {
	case reflect.Struct:
		if !bytes.HasPrefix(trimmed, []byte("{")) {
			return data, nil
		}
		o, err := ojson.Parse(trimmed)
		if err != nil {
			return nil, err
		}
		fields := jsonFields(t)
		for _, key := range o.Keys() {
			ft, ok := fields[key]
			if !ok {
				o.Delete(key)
				continue
			}
			raw, _ := o.Get(key)
			exact, err := exactKeys(raw, ft)
			if err != nil {
				return nil, err
			}
			o.Set(key, exact)
		}
		return o.MarshalJSON()
	case reflect.Map:
		if !bytes.HasPrefix(trimmed, []byte("{")) {
			return data, nil
		}
		o, err := ojson.Parse(trimmed)
		if err != nil {
			return nil, err
		}
		for _, key := range o.Keys() {
			raw, _ := o.Get(key)
			exact, err := exactKeys(raw, t.Elem())
			if err != nil {
				return nil, err
			}
			o.Set(key, exact)
		}
		return o.MarshalJSON()
	case reflect.Slice, reflect.Array:
		if !bytes.HasPrefix(trimmed, []byte("[")) {
			return data, nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, raw := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			exact, err := exactKeys(raw, t.Elem())
			if err != nil {
				return nil, err
			}
			buf.Write(exact)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	}
	return data, nil
}

// hasStructs reports whether a value of t may hold an object that
// encoding/json reads into a struct by its fields.
func hasStructs(t reflect.Type, seen map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType || reflect.PointerTo(t).Implements(unmarshalerType) {
		return false
	}
	if seen[t] {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		return true
	case reflect.Map, reflect.Slice, reflect.Array:
		seen[t] = true
		return hasStructs(t.Elem(), seen)
	}
	return false
}

// jsonFields returns the types of the fields of struct t by the names
// encoding/json reads them under, with the fields of embedded structs that
// have no name of their own, as encoding/json promotes them.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range jsonFields(ft) {
					if _, ok := fields[k]; !ok {
						fields[k] = v
					}
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields
}
