package jobs

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Masked is what a secret becomes in job output.
const Masked = "$encrypted$"

// minMasked is the shortest value masked: shorter ones, such as a PIN,
// would hide ordinary text.
const minMasked = 4

// Mask hides values in the events emitted from now on: in their stdout,
// task and play names, and event data. Each value is masked as given,
// JSON-escaped once or twice (as Go and Python write it), and, when it
// spans lines, line by line.
func (o *Output) Mask(values ...string) {
	o.maskMu.Lock()
	defer o.maskMu.Unlock()
	if o.secrets == nil {
		o.secrets = map[string]struct{}{}
	}
	added := false
	for _, v := range values {
		for _, f := range maskForms(v) {
			if _, ok := o.secrets[f]; !ok {
				o.secrets[f] = struct{}{}
				added = true
			}
		}
	}
	if !added {
		return
	}
	forms := make([]string, 0, len(o.secrets))
	for f := range o.secrets {
		forms = append(forms, f)
	}
	// strings.Replacer tries its patterns in argument order at each
	// position: longest first makes a secret that contains another masked
	// whole.
	slices.SortFunc(forms, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, Masked)
	}
	o.replacer.Store(strings.NewReplacer(pairs...))
}

func maskForms(v string) []string {
	var out []string
	add := func(s string) {
		if utf8.RuneCountInString(s) >= minMasked {
			out = append(out, s)
		}
	}
	add(v)
	for _, once := range []string{jsonEscape(v, false), jsonEscape(v, true)} {
		add(once)
		// A JSON text inside a printed result, such as a to_json in a
		// debug message, is escaped again.
		add(jsonEscape(once, false))
	}
	if strings.ContainsAny(v, "\r\n") {
		for _, line := range strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == '\r' }) {
			add(strings.TrimSpace(line))
		}
	}
	return out
}

// jsonEscape is v inside a JSON string, without the quotes. ascii escapes
// non-ASCII characters as Python's json.dumps does by default.
func jsonEscape(v string, ascii bool) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || (ascii && r > 0x7e):
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// masked returns e with the secrets replaced. Its event data is copied:
// the runner may still read the original (set_stats, host summaries).
func (o *Output) masked(e Event) Event {
	r := o.replacer.Load()
	if r == nil {
		return e
	}
	e.Stdout = r.Replace(e.Stdout)
	e.Task = r.Replace(e.Task)
	e.Play = r.Replace(e.Play)
	if e.EventData != nil {
		e.EventData = maskValue(r, e.EventData).(map[string]any)
	}
	return e
}

func maskValue(r *strings.Replacer, v any) any {
	switch x := v.(type) {
	case string:
		return r.Replace(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, y := range x {
			out[r.Replace(k)] = maskValue(r, y)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = maskValue(r, y)
		}
		return out
	}
	return v
}

type outputKey struct{}

// WithOutput makes the secrets resolved with the returned context
// (MaskSecrets) masked in out.
func WithOutput(ctx context.Context, out *Output) context.Context {
	return context.WithValue(ctx, outputKey{}, out)
}

// MaskSecrets masks values in the output of the job ctx runs, if any.
func MaskSecrets(ctx context.Context, values ...string) {
	if out, ok := ctx.Value(outputKey{}).(*Output); ok && out != nil {
		out.Mask(values...)
	}
}
