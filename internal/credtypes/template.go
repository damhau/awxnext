package credtypes

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/nikolalohinski/gonja/v2/builtins"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
)

// Injector templates are Jinja2, rendered with gonja the way AWX renders them
// in its sandboxed environment: input fields by id, tower.filename (or
// awx.filename) for the unnamed file injector and tower.filename.NAME for
// named ones. Templates cannot include or import others, range() is capped
// as in Jinja's sandbox, and output is bounded.
const (
	maxRange  = 100_000 // jinja2.sandbox.MAX_RANGE
	maxOutput = 4 << 20
	rootName  = "/injector"
)

var env = &exec.Environment{
	Context:           globals(),
	Filters:           filters(),
	Tests:             builtins.Tests,
	ControlStructures: builtins.ControlStructures,
	Methods:           builtins.Methods,
}

func filters() *exec.FilterSet {
	fs := exec.NewFilterSet(map[string]exec.FilterFunction{}).Update(builtins.Filters)
	if err := fs.Replace("tojson", filterToJSON); err != nil {
		panic(err)
	}
	return fs
}

// globals keeps Jinja's global functions that only build values.
func globals() *exec.Context {
	ctx := exec.EmptyContext()
	for _, name := range []string{"dict", "namespace", "cycler", "joiner"} {
		if f, ok := builtins.GlobalFunctions.Get(name); ok {
			ctx.Set(name, f)
		}
	}
	ctx.Set("range", boundedRange)
	return ctx
}

func boundedRange(_ *exec.Evaluator, params *exec.VarArgs) ([]int, error) {
	start, stop, step := 0, 0, 1
	ints := make([]int, len(params.Args))
	for i, a := range params.Args {
		if !a.IsInteger() {
			return nil, exec.ErrInvalidCall(errors.New("range() arguments must be integers"))
		}
		ints[i] = a.Integer()
	}
	switch len(ints) {
	case 1:
		stop = ints[0]
	case 2:
		start, stop = ints[0], ints[1]
	case 3:
		start, stop, step = ints[0], ints[1], ints[2]
	default:
		return nil, exec.ErrInvalidCall(errors.New("expected signature is [start, ]stop[, step]"))
	}
	if step == 0 {
		return nil, exec.ErrInvalidCall(errors.New("range() step must not be zero"))
	}
	n := 0
	if step > 0 && stop > start {
		n = (stop - start + step - 1) / step
	} else if step < 0 && start > stop {
		n = (start - stop - step - 1) / -step
	}
	if n > maxRange {
		return nil, exec.ErrInvalidCall(fmt.Errorf("range too big, maximum size for range is %d", maxRange))
	}
	out := make([]int, n)
	for i := range out {
		out[i] = start + i*step
	}
	return out, nil
}

// Render evaluates an injector template. Undefined names render empty.
func Render(tmpl string, vars map[string]any, files map[string]string) (string, error) {
	ctx := map[string]any{}
	for k, v := range vars {
		ctx[k] = pyValue(v)
	}
	ns := map[string]any{}
	if path, ok := files[""]; ok {
		ns["filename"] = path
	} else if len(files) > 0 {
		named := map[string]any{}
		for k, path := range files {
			named[k] = path
		}
		ns["filename"] = named
	}
	ctx["tower"], ctx["awx"] = ns, ns
	return execute(tmpl, ctx, false)
}

// Check renders a template as AWX checks it when a type is saved: every
// input field is "EXAMPLE", the declared file injectors have names, and an
// undefined name is an error.
func Check(tmpl string, fields []string, files map[string]bool) error {
	ctx := map[string]any{}
	for _, id := range fields {
		ctx[id] = "EXAMPLE"
	}
	ns := map[string]any{}
	if files[""] {
		ns["filename"] = "EXAMPLE_FILENAME"
	} else if len(files) > 0 {
		named := map[string]any{}
		for k := range files {
			named[k] = "EXAMPLE_FILENAME"
		}
		ns["filename"] = named
	}
	ctx["tower"], ctx["awx"] = ns, ns
	_, err := execute(tmpl, ctx, true)
	return err
}

func execute(tmpl string, data map[string]any, strict bool) (string, error) {
	cfg := config.New()
	cfg.StrictUndefined = strict
	loader, err := loaders.NewMemoryLoader(map[string]string{rootName: tmpl})
	if err != nil {
		return "", err
	}
	t, err := exec.NewTemplate(rootName, cfg, loader, env)
	if err != nil {
		return "", cleanError(err)
	}
	var b strings.Builder
	if err := t.Execute(&limitWriter{w: &b, left: maxOutput}, exec.NewContext(data)); err != nil {
		return "", cleanError(err)
	}
	return b.String(), nil
}

// cleanError drops the internal template name from gonja's messages.
func cleanError(err error) error {
	msg := strings.ReplaceAll(err.Error(), rootName, "template")
	return errors.New(strings.TrimPrefix(msg, "failed to reader template 'template': "))
}

// pyValue gives JSON numbers Python's types: whole numbers are ints, so they
// print as 5 and not 5.0.
func pyValue(v any) any {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int(x)
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = pyValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pyValue(e)
		}
		return out
	}
	return v
}

type limitWriter struct {
	w    io.Writer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, fmt.Errorf("template output is larger than %d MiB", maxOutput>>20)
	}
	l.left -= len(p)
	return l.w.Write(p)
}
