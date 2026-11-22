// Package launchpolicy holds what launch policies (roadmap M12-07) compute
// without the database: their CEL condition, checked against the fixed
// shape of a launch and evaluated over one.
package launchpolicy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
)

const (
	costLimit   = 1_000_000
	evalTimeout = time.Second
	// MaxLength is the longest condition a policy takes.
	MaxLength = 4096
)

var (
	str     = types.StringType
	integer = types.IntType
	boolean = types.BoolType
	strList = types.NewListType(types.StringType)
)

func object(name string) *types.Type { return types.NewObjectType("object(" + name + ")") }

// Field is one field of a launch, as the editor lists them.
type Field struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Help string `json:"help"`
}

type field struct {
	name string
	typ  *types.Type
	help string
}

// objects are the launch's object types, by name. A field they don't list
// is an error when the condition is saved.
var objects = map[string][]field{
	"launch": {
		{"type", str, `What is launched: "job", "workflow_job" or "ad_hoc_command".`},
		{"template", object("template"), "The template launched. Its id is 0 for an ad hoc command."},
		{"labels", strList, "The job's labels: the template's and those given at launch."},
		{"project", object("project"), "The job's project. Its id is 0 for a workflow or an ad hoc command."},
		{"playbook", str, "The playbook."},
		{"inventory", object("inventory"), "The inventory. Its id is 0 when there is none, as for a workflow without one."},
		{"limit", str, `The limit, "" for none.`},
		{"job_type", str, `"run" or "check".`},
		{"verbosity", integer, "The verbosity, 0 to 5."},
		{"diff_mode", boolean, "Whether diff mode is on."},
		{"forks", integer, "The forks, 0 for the default."},
		{"image", str, `The execution environment's image, "" when there is none.`},
		{"credentials", types.NewListType(object("credential")), "The job's credentials."},
		{"instance_groups", strList, "The instance groups the launch asks for, in order: those given at launch, or else the template's."},
		{"workflow", object("workflow"), "The workflow job that launches this one as a node. Its id is 0 when there is none."},
		{"extra_vars", types.NewMapType(str, types.DynType), `The extra vars after the survey, with passwords as "$encrypted$".`},
		{"branch", str, `The branch given at launch, "" when none was.`},
		{"user", object("user"), "Who launched it. Its id is 0 when no user did, as for a webhook."},
		{"launch_type", str, `How it was launched: "manual", "relaunch", "callback", "scheduled", "workflow", "webhook"...`},
		{"schedule", str, `The schedule's name, "" when no schedule launched it.`},
		{"webhook", str, `The webhook's service, such as "github", "" when no webhook launched it.`},
		{"request", object("request"), "The portal request it runs for. Its id is 0 when there is none."},
		{"event_rule", str, `The event rule's name, "" when no event rule launched it.`},
		{"module", object("module"), `An ad hoc command's module, "" otherwise.`},
	},
	"template": {
		{"id", integer, ""},
		{"name", str, ""},
		{"type", str, `"job_template" or "workflow_job_template", "" for an ad hoc command.`},
		{"organization", str, "The organization's name."},
		{"labels", strList, "The template's labels."},
		{"branch", str, "The template's branch."},
	},
	"project": {
		{"id", integer, ""},
		{"name", str, ""},
		{"url", str, "The source control URL."},
		{"branch", str, "The project's branch."},
		{"revision", str, "The revision of its last update."},
	},
	"inventory": {
		{"id", integer, ""},
		{"name", str, ""},
		{"organization", str, "The organization's name."},
		{"labels", strList, "The inventory's labels."},
		{"hosts", integer, "Its number of hosts."},
	},
	"credential": {
		{"name", str, ""},
		{"type", str, `The credential type's name, such as "Machine".`},
		{"kind", str, `The credential type's kind, such as "ssh" or "cloud".`},
	},
	"workflow": {
		{"id", integer, ""},
		{"name", str, ""},
	},
	"user": {
		{"id", integer, ""},
		{"username", str, ""},
		{"teams", strList, "The names of the teams the user is a member of."},
		{"superuser", boolean, ""},
	},
	"request": {
		{"id", integer, ""},
		{"item", str, "The catalog item's name."},
	},
	"module": {
		{"name", str, ""},
		{"args", str, ""},
	},
}

// Fields lists the launch's fields, and now, for the editor.
func Fields() []Field {
	var out []Field
	var walk func(obj, path string)
	walk = func(obj, path string) {
		for _, f := range objects[obj] {
			p := path + "." + f.name
			name := typeName(f.typ)
			out = append(out, Field{Path: p, Type: name, Help: f.help})
			if sub, ok := strings.CutPrefix(f.typ.TypeName(), "object("); ok && f.typ.Kind() == types.StructKind {
				walk(strings.TrimSuffix(sub, ")"), p)
			} else if f.typ.Kind() == types.ListKind {
				if elem := f.typ.Parameters()[0]; elem.Kind() == types.StructKind {
					walk(strings.TrimSuffix(strings.TrimPrefix(elem.TypeName(), "object("), ")"), p+"[]")
				}
			}
		}
	}
	walk("launch", "launch")
	out = append(out, Field{Path: "now", Type: "timestamp", Help: "When the launch is checked."})
	return out
}

func typeName(t *types.Type) string {
	switch t.Kind() {
	case types.StructKind:
		return "object"
	case types.ListKind:
		return "list of " + typeName(t.Parameters()[0])
	case types.MapKind:
		return "map"
	}
	return t.String()
}

// provider declares the launch's object types to the checker.
type provider struct{ *types.Registry }

func objectFields(name string) ([]field, bool) {
	inner, ok := strings.CutPrefix(name, "object(")
	if !ok {
		return nil, false
	}
	fs, ok := objects[strings.TrimSuffix(inner, ")")]
	return fs, ok
}

func (p provider) FindStructType(name string) (*types.Type, bool) {
	if _, ok := objectFields(name); ok {
		return types.NewTypeTypeWithParam(types.NewObjectType(name)), true
	}
	return p.Registry.FindStructType(name)
}

func (p provider) FindStructFieldNames(name string) ([]string, bool) {
	fs, ok := objectFields(name)
	if !ok {
		return p.Registry.FindStructFieldNames(name)
	}
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.name
	}
	sort.Strings(names)
	return names, true
}

func (p provider) FindStructFieldType(name, fieldName string) (*types.FieldType, bool) {
	fs, ok := objectFields(name)
	if !ok {
		return p.Registry.FindStructFieldType(name, fieldName)
	}
	for _, f := range fs {
		if f.name == fieldName {
			return &types.FieldType{Type: f.typ}, true
		}
	}
	return nil, false
}

func libraries() []cel.EnvOption {
	return []cel.EnvOption{
		ext.Strings(), ext.Lists(), ext.Math(), ext.Sets(),
		cel.OptionalTypes(),
		cel.CrossTypeNumericComparisons(true),
		cel.Variable("now", cel.TimestampType),
		cel.Function("window",
			cel.Overload("window_timestamp_string", []*cel.Type{cel.TimestampType, cel.StringType}, cel.BoolType,
				cel.BinaryBinding(func(t, spec ref.Val) ref.Val { return windowVal(t, spec, types.String("UTC")) })),
			cel.Overload("window_timestamp_string_string", []*cel.Type{cel.TimestampType, cel.StringType, cel.StringType}, cel.BoolType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val { return windowVal(args[0], args[1], args[2]) }))),
	}
}

func windowVal(t, spec, zone ref.Val) ref.Val {
	ts, ok := t.Value().(time.Time)
	s, ok2 := spec.Value().(string)
	z, ok3 := zone.Value().(string)
	if !ok || !ok2 || !ok3 {
		return types.NewErr("window takes a timestamp, a window and a time zone")
	}
	in, err := InWindow(ts, s, z)
	if err != nil {
		return types.WrapErr(err)
	}
	return types.Bool(in)
}

var checkEnv = func() *cel.Env {
	reg, err := types.NewRegistry()
	if err != nil {
		panic(err)
	}
	env, err := cel.NewEnv(append(libraries(), cel.CustomTypeProvider(provider{reg}), cel.Variable("launch", object("launch")))...)
	if err != nil {
		panic(err)
	}
	return env
}()

// runtimeEnv evaluates conditions already checked: launch is a map.
var runtimeEnv = func() *cel.Env {
	env, err := cel.NewEnv(append(libraries(), cel.Variable("launch", cel.MapType(cel.StringType, cel.DynType)))...)
	if err != nil {
		panic(err)
	}
	return env
}()

// Check compiles a condition: it must name only the launch's fields, give
// true or false, and its literal windows and time zones must be valid.
func Check(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return errors.New("The condition is empty.")
	}
	if len(expr) > MaxLength {
		return fmt.Errorf("The condition is longer than %d characters.", MaxLength)
	}
	a, iss := checkEnv.Compile(expr)
	if iss.Err() != nil {
		return errors.New(iss.Err().Error())
	}
	if a.OutputType() != cel.BoolType {
		return fmt.Errorf("The condition must be true or false, not %s.", a.OutputType())
	}
	var bad error
	ast.PreOrderVisit(a.NativeRep().Expr(), ast.NewExprVisitor(func(e ast.Expr) {
		if bad != nil || e.Kind() != ast.CallKind || e.AsCall().FunctionName() != "window" {
			return
		}
		args := e.AsCall().Args()
		lit := func(i int) (string, bool) {
			if i >= len(args) || args[i].Kind() != ast.LiteralKind {
				return "", false
			}
			s, ok := args[i].AsLiteral().Value().(string)
			return s, ok
		}
		if spec, ok := lit(1); ok {
			if _, err := parseWindow(spec); err != nil {
				bad = err
			}
		}
		if zone, ok := lit(2); ok {
			if _, err := time.LoadLocation(zone); err != nil {
				bad = fmt.Errorf("Unknown time zone %q.", zone)
			}
		}
	}))
	return bad
}

var programs = struct {
	sync.Mutex
	m map[string]cel.Program
}{m: map[string]cel.Program{}}

func compile(expr string) (cel.Program, error) {
	programs.Lock()
	prg, ok := programs.m[expr]
	programs.Unlock()
	if ok {
		return prg, nil
	}
	a, iss := runtimeEnv.Compile(expr)
	if iss.Err() != nil {
		return nil, errors.New(iss.Err().Error())
	}
	prg, err := runtimeEnv.Program(a, cel.CostLimit(costLimit), cel.InterruptCheckFrequency(100))
	if err != nil {
		return nil, err
	}
	programs.Lock()
	if len(programs.m) > 10000 {
		programs.m = map[string]cel.Program{}
	}
	programs.m[expr] = prg
	programs.Unlock()
	return prg, nil
}

// Refuses evaluates a condition over a launch: true when the policy
// refuses it.
func Refuses(ctx context.Context, expr string, launch map[string]any, now time.Time) (bool, error) {
	prg, err := compile(expr)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	out, _, err := prg.ContextEval(ctx, map[string]any{"launch": launch, "now": now})
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("the condition gave %v, not true or false", out)
	}
	return b, nil
}
