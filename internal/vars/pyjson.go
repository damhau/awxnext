package vars

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// PyJSON writes v as Python's json.dumps does with sorted keys: ", " and
// ": " separators and ASCII output. AWX stores a job's extra_vars this way,
// and clients compare them as text.
func PyJSON(v any) string {
	var b strings.Builder
	if err := WritePyJSON(&b, v, -1, nil); err != nil {
		return MustJSON(v)
	}
	return b.String()
}

// WritePyJSON writes v as json.dumps(v, sort_keys=True, indent=indent);
// indent < 0 is no indent. unwrap, when set, converts values of a template
// engine to plain Go values first.
func WritePyJSON(b *strings.Builder, v any, indent int, unwrap func(any) any) error {
	return pyJSON(b, v, indent, 0, unwrap)
}

func pyJSON(b *strings.Builder, v any, indent, depth int, unwrap func(any) any) error {
	if unwrap != nil {
		v = unwrap(v)
	}
	if v == nil {
		b.WriteString("null")
		return nil
	}
	switch x := v.(type) {
	case bool:
		b.WriteString(strconv.FormatBool(x))
		return nil
	case string:
		pyString(b, x)
		return nil
	case json.Number:
		b.WriteString(x.String())
		return nil
	case float32, float64:
		f := reflect.ValueOf(x).Float()
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eIN") {
			s += ".0"
		}
		b.WriteString(s)
		return nil
	}
	rv := reflect.ValueOf(v)
	newline := func(d int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*d))
		}
	}
	sep := ", "
	if indent >= 0 {
		sep = ","
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(rv.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(rv.Uint(), 10))
	case reflect.Slice, reflect.Array:
		if rv.Len() == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				b.WriteString(sep)
			}
			newline(depth + 1)
			if err := pyJSON(b, rv.Index(i).Interface(), indent, depth+1, unwrap); err != nil {
				return err
			}
		}
		newline(depth)
		b.WriteByte(']')
	case reflect.Map:
		if rv.Len() == 0 {
			b.WriteString("{}")
			return nil
		}
		keys := make([]string, 0, rv.Len())
		vals := map[string]reflect.Value{}
		for _, k := range rv.MapKeys() {
			ks := fmt.Sprint(k.Interface())
			keys = append(keys, ks)
			vals[ks] = rv.MapIndex(k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(sep)
			}
			newline(depth + 1)
			pyString(b, k)
			b.WriteString(": ")
			if err := pyJSON(b, vals[k].Interface(), indent, depth+1, unwrap); err != nil {
				return err
			}
		}
		newline(depth)
		b.WriteByte('}')
	default:
		return fmt.Errorf("Object of type %T is not JSON serializable", v)
	}
	return nil
}

// pyString writes a JSON string as Python's json.dumps does with ensure_ascii.
func pyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || (r >= 0x7f && r <= 0xffff):
				fmt.Fprintf(b, `\u%04x`, r)
			case r > 0xffff:
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
