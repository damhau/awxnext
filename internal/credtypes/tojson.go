package credtypes

import (
	"errors"
	"strings"

	"github.com/damhau/awxnext/internal/vars"
	"github.com/nikolalohinski/gonja/v2/exec"
)

// filterToJSON is Jinja's tojson (htmlsafe_json_dumps): Python's json.dumps
// with sorted keys, its separators and ASCII output, then <, >, & and '
// escaped. gonja's own writes compact Go JSON.
func filterToJSON(_ *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
	if in.IsError() {
		return in
	}
	indent := -1
	if v, ok := params.KwArgs["indent"]; ok && !v.IsNil() {
		if !v.IsInteger() {
			return exec.AsValue(exec.ErrInvalidCall(errors.New("tojson: indent must be an integer")))
		}
		indent = v.Integer()
	} else if len(params.Args) > 0 && params.Args[0].IsInteger() {
		indent = params.Args[0].Integer()
	}
	var b strings.Builder
	unwrap := func(v any) any {
		if ev, ok := v.(*exec.Value); ok {
			return ev.Interface()
		}
		return v
	}
	if err := vars.WritePyJSON(&b, in.Interface(), indent, unwrap); err != nil {
		return exec.AsValue(exec.ErrInvalidCall(err))
	}
	return exec.AsSafeValue(strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`, "'", `\u0027`).Replace(b.String()))
}
