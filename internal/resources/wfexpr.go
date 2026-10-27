package resources

import (
	"fmt"
	"reflect"

	"github.com/expr-lang/expr"
)

// Workflow node conditions ("when") and loop lists are expressions in the expr
// language (https://expr-lang.org), which reads like Ansible's when: clauses:
//
//	deploy_env == 'prod' and replicas > 2
//	'web' in target_groups
//	nodes.build.status == 'successful' and nodes.build.artifacts.image_tag != nil
//	nodes['smoke-test']?.status ?? 'skipped'
//
// Undefined variables are nil rather than errors.

// checkExpr reports a syntax error in a workflow expression.
func checkExpr(src string) error {
	_, err := expr.Compile(src, expr.AllowUndefinedVariables())
	return err
}

func evalExpr(src string, env map[string]any) (any, error) {
	p, err := expr.Compile(src, expr.AllowUndefinedVariables())
	if err != nil {
		return nil, err
	}
	return expr.Run(p, env)
}

// evalWhen evaluates a condition with Jinja's notion of truth: nil, false,
// zero, "" and empty collections are false.
func evalWhen(src string, env map[string]any) (bool, error) {
	v, err := evalExpr(src, env)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

// evalLoop evaluates a loop expression, which must give a list.
func evalLoop(src string, env map[string]any) ([]any, error) {
	v, err := evalExpr(src, env)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("the loop expression gave nothing (is the variable defined?)")
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("the loop expression must give a list, not %T", v)
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, nil
}

func truthy(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool()
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() != 0
	}
	return true
}
