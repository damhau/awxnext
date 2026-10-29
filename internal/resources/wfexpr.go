package resources

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/outputs"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
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

// artifactRef is a reference to a node's output in an expression:
// nodes.<node>.artifacts.<output>, written with dots, brackets or ?.
type artifactRef struct{ node, output string }

func matchArtifactRef(n ast.Node) (artifactRef, bool) {
	prop := func(n ast.Node) (*ast.MemberNode, string, bool) {
		m, ok := n.(*ast.MemberNode)
		if !ok {
			return nil, "", false
		}
		s, ok := m.Property.(*ast.StringNode)
		if !ok {
			return nil, "", false
		}
		return m, s.Value, true
	}
	m, output, ok := prop(n)
	if !ok {
		return artifactRef{}, false
	}
	a, field, ok := prop(m.Node)
	if !ok || field != "artifacts" {
		return artifactRef{}, false
	}
	nd, node, ok := prop(a.Node)
	if !ok {
		return artifactRef{}, false
	}
	if root, ok := nd.Node.(*ast.IdentifierNode); !ok || root.Value != "nodes" {
		return artifactRef{}, false
	}
	return artifactRef{node, output}, true
}

type refCollector []artifactRef

func (r *refCollector) Visit(n *ast.Node) {
	if ref, ok := matchArtifactRef(*n); ok {
		*r = append(*r, ref)
	}
}

// checkOutputRefs checks a node's condition and loop (field -> expression)
// against the outputs the templates of the workflow's nodes declare: a
// reference to an output a node does not declare, or a loop over an output
// that is not a list, is refused. Nodes whose template declares no outputs,
// and plain variables, are not checked.
func checkOutputRefs(c *Ctx, wfID int64, exprs map[string]string) error {
	declared := map[string][]outputs.Decl{}
	rows, err := c.DB().Query(c, `SELECT n.identifier, t.outputs FROM workflow_job_template_nodes n
		JOIN unified_job_templates t ON t.id = n.unified_job_template_id
		WHERE n.workflow_job_template_id = $1 AND t.type = 'job_template'`, wfID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ident string
		var raw []byte
		if err := rows.Scan(&ident, &raw); err != nil {
			rows.Close()
			return err
		}
		if d := outputs.Load(raw); len(d) > 0 {
			declared[ident] = d
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, field := range []string{"when", "loop"} {
		src := exprs[field]
		if src == "" {
			continue
		}
		tree, err := parser.Parse(src)
		if err != nil {
			continue // reported by the field's own check
		}
		var refs refCollector
		ast.Walk(&tree.Node, &refs)
		for _, r := range refs {
			if d := declared[r.node]; len(d) > 0 {
				if _, ok := outputs.Find(d, r.output); !ok {
					return api.FieldError(field, fmt.Sprintf("Step %q has no output %q; its template declares %s.", r.node, r.output, strings.Join(outputs.Names(d), ", ")))
				}
			}
		}
		if field != "loop" {
			continue
		}
		root := tree.Node
		if ch, ok := root.(*ast.ChainNode); ok {
			root = ch.Node
		}
		if r, ok := matchArtifactRef(root); ok {
			if d, ok := outputs.Find(declared[r.node], r.output); ok && d.Type != "list" {
				return api.FieldError("loop", fmt.Sprintf("Output %q of step %q is %s; a loop needs a list.", r.output, r.node, outputs.Article(d.Type)))
			}
		}
	}
	return nil
}
