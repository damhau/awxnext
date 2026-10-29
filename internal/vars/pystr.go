package vars

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// PyStr writes v as Python's str() does: strings as they are, True/False,
// None, and lists and dicts in their repr (dict keys sorted, as Go maps have
// no order). AWX turns variables into environment values this way.
func PyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	pyRepr(&b, v)
	return b.String()
}

func pyRepr(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if x {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case string:
		// Python quotes with ' unless the string holds one and no ".
		q := "'"
		if strings.Contains(x, "'") && !strings.Contains(x, `"`) {
			q = `"`
		}
		r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`, q, `\`+q)
		b.WriteString(q + r.Replace(x) + q)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			b.WriteString(strconv.FormatInt(int64(x), 10))
		} else {
			b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case []any:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyRepr(b, e)
		}
		b.WriteString("]")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyRepr(b, k)
			b.WriteString(": ")
			pyRepr(b, x[k])
		}
		b.WriteString("}")
	default:
		fmt.Fprint(b, x)
	}
}
