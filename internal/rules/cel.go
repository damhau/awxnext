// Package rules holds what event rules (roadmap M14-02) compute without the
// database: their CEL expressions, checked against the schema inferred
// from a stream's events and evaluated over each event, and the
// throttling that ansible-rulebook's once_within and once_after do.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

var jsonValueType = reflect.TypeOf(&structpb.Value{})

// Want is the type an expression must have.
type Want int

const (
	// Any value: a variable mapped from the event.
	Any Want = iota
	// Bool: a filter.
	Bool
	// String: a key, a limit, an event type.
	String
)

func (w Want) String() string {
	switch w {
	case Bool:
		return "true or false"
	case String:
		return "a string"
	}
	return "a value"
}

const (
	// costLimit bounds what one evaluation may do: comprehensions over a
	// large list of alerts fit; a runaway expression stops.
	costLimit   = 1_000_000
	evalTimeout = time.Second
	// MaxLength is the longest expression a rule takes.
	MaxLength = 4096
)

func libraries() []cel.EnvOption {
	return []cel.EnvOption{
		ext.Strings(), ext.Lists(), ext.Math(), ext.Encoders(), ext.Sets(),
		cel.OptionalTypes(),
		cel.CrossTypeNumericComparisons(true),
	}
}

// runtimeEnv evaluates expressions already checked: event is any map.
var runtimeEnv = func() *cel.Env {
	ev := cel.MapType(cel.StringType, cel.DynType)
	env, err := cel.NewEnv(append(libraries(), cel.Variable("event", ev), cel.Variable("events", cel.ListType(ev)))...)
	if err != nil {
		panic(err)
	}
	return env
}()

// Check compiles expr and checks its types against the schema: a field the
// schema knows must be used as what it is, a field it doesn't know is
// dynamic, and the result must be what want asks.
func Check(s *Schema, expr string, want Want) error {
	if strings.TrimSpace(expr) == "" {
		return errors.New("The expression is empty.")
	}
	if len(expr) > MaxLength {
		return fmt.Errorf("The expression is longer than %d characters.", MaxLength)
	}
	t := newTyper()
	env, err := cel.NewEnv(append(libraries(), t.options(s)...)...)
	if err != nil {
		return err
	}
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return errors.New(iss.Err().Error())
	}
	out := ast.OutputType()
	if out == cel.DynType || out.Kind() == types.TypeParamKind {
		return nil
	}
	switch want {
	case Bool:
		if out != cel.BoolType {
			return fmt.Errorf("The expression must be %s, not %s.", want, out)
		}
	case String:
		if out != cel.StringType {
			return fmt.Errorf("The expression must be %s, not %s: use string(...).", want, out)
		}
	}
	return nil
}

var programs = struct {
	sync.Mutex
	m map[string]cel.Program
}{m: map[string]cel.Program{}}

// Compile returns the program of an expression, cached.
func Compile(expr string) (cel.Program, error) {
	programs.Lock()
	prg, ok := programs.m[expr]
	programs.Unlock()
	if ok {
		return prg, nil
	}
	ast, iss := runtimeEnv.Compile(expr)
	if iss.Err() != nil {
		return nil, errors.New(iss.Err().Error())
	}
	prg, err := runtimeEnv.Program(ast, cel.CostLimit(costLimit), cel.InterruptCheckFrequency(100))
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

// Vars are the variables an expression sees: the event, and the events of
// an action that several fire (once_after), the first of them as event.
type Vars struct {
	Event  map[string]any
	Events []map[string]any
}

func (v Vars) activation() map[string]any {
	events := make([]any, 0, len(v.Events))
	for _, e := range v.Events {
		events = append(events, e)
	}
	if len(events) == 0 {
		events = append(events, v.Event)
	}
	return map[string]any{"event": v.Event, "events": events}
}

// Eval evaluates an expression and returns its value as JSON would hold
// it: strings, float64 or int64 numbers, bools, maps, lists, nil.
func Eval(ctx context.Context, expr string, v Vars) (any, error) {
	prg, err := Compile(expr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	out, _, err := prg.ContextEval(ctx, v.activation())
	if err != nil {
		return nil, err
	}
	return native(out)
}

// Match evaluates a filter: true when it holds. An error, such as a field
// the event lacks, is no match.
func Match(ctx context.Context, expr string, v Vars) (bool, error) {
	out, err := Eval(ctx, expr, v)
	if err != nil {
		return false, err
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("the filter gave %T, not true or false", out)
	}
	return b, nil
}

// Text evaluates an expression that gives a string: a key or a limit.
func Text(ctx context.Context, expr string, v Vars) (string, error) {
	out, err := Eval(ctx, expr, v)
	if err != nil {
		return "", err
	}
	switch x := out.(type) {
	case string:
		return x, nil
	case nil:
		return "", nil
	}
	return "", fmt.Errorf("the expression gave %T, not a string", out)
}

func native(v ref.Val) (any, error) {
	if types.IsUnknownOrError(v) {
		return nil, fmt.Errorf("%v", v)
	}
	switch x := v.(type) {
	case types.Bool:
		return bool(x), nil
	case types.String:
		return string(x), nil
	case types.Int:
		return int64(x), nil
	case types.Double:
		return float64(x), nil
	case types.Null:
		return nil, nil
	}
	j, err := v.ConvertToNative(jsonValueType)
	if err != nil {
		return nil, err
	}
	b, err := protojson.Marshal(j.(*structpb.Value))
	if err != nil {
		return nil, err
	}
	var out any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	return plain(out), nil
}

// plain turns JSON numbers into int64 when they are whole, float64
// otherwise, as a launch's variables are written back.
func plain(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = plain(e)
		}
	case []any:
		for i, e := range x {
			x[i] = plain(e)
		}
	}
	return v
}
