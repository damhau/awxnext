package resources

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"

	"github.com/damhau/awxnext/internal/api"
)

// Computed questions (awxnext, roadmap M15-03): a question with value_from
// is never asked. The server computes its answer at launch with an expr
// expression over the earlier answers, the items picked in lists, the user,
// their team and, for a portal request, the request (docs/surveys.md).

const (
	minGeneratedPassword = 8
	maxGeneratedPassword = 128
	passwordAlphabet     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

var computedTypes = map[string]bool{"text": true, "textarea": true, "password": true, "integer": true, "float": true}

// computeEnv is what a value_from expression reads.
type computeEnv struct {
	answers map[string]any
	picked  map[string]any
	user    map[string]any
	team    map[string]any
	request map[string]any
}

func (e *computeEnv) vars() map[string]any {
	env := map[string]any{"answers": e.answers, "user": e.user, "team": nil, "request": nil}
	// A nil map would read as an empty one: request.id would be nil rather
	// than an error outside a request.
	if e.team != nil {
		env["team"] = e.team
	}
	if e.request != nil {
		env["request"] = e.request
	}
	return env
}

func computeOptions(e *computeEnv) []expr.Option {
	return []expr.Option{
		expr.Env(map[string]any{"answers": map[string]any{}, "user": map[string]any{}, "team": map[string]any{}, "request": map[string]any{}}),
		expr.Function("item", func(params ...any) (any, error) {
			name, _ := params[0].(string)
			return e.picked[name], nil
		}, new(func(string) any)),
		expr.Function("password", func(params ...any) (any, error) {
			n, _ := params[0].(int)
			return generatePassword(n)
		}, new(func(int) string)),
	}
}

func generatePassword(n int) (string, error) {
	if n < minGeneratedPassword || n > maxGeneratedPassword {
		return "", fmt.Errorf("password(%d): the length must be between %d and %d", n, minGeneratedPassword, maxGeneratedPassword)
	}
	b := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[k.Int64()]
	}
	return string(b), nil
}

// checkValueFrom checks a computed question when its survey is saved.
// earlier are the types of the questions before it, by variable.
func checkValueFrom(qm map[string]any, earlier map[string]string, i int) error {
	src, ok := qm["value_from"].(string)
	if !ok {
		return fmt.Errorf("'value_from' in survey question %d must be an expression.", i)
	}
	typ := str(qm["type"])
	if !computedTypes[typ] {
		return fmt.Errorf("'value_from' in survey question %d needs a text, textarea, password, integer or float question.", i)
	}
	if d := qm["default"]; d != nil && d != "" {
		return fmt.Errorf("Survey question %d is computed (value_from): it takes no default.", i)
	}
	tree, err := parser.Parse(src)
	if err != nil {
		return fmt.Errorf("'value_from' in survey question %d: %v", i, err)
	}
	if _, err := expr.Compile(src, computeOptions(&computeEnv{})...); err != nil {
		return fmt.Errorf("'value_from' in survey question %d: %v", i, err)
	}
	ch := &valueFromCheck{earlier: earlier, password: typ == "password"}
	ast.Walk(&tree.Node, ch)
	if ch.err != nil {
		return fmt.Errorf("'value_from' in survey question %d: %v", i, ch.err)
	}
	return nil
}

// valueFromCheck refuses what an expression would read in vain: an item or
// an answer of no earlier question, or of a password question, which
// computed answers never see.
type valueFromCheck struct {
	earlier  map[string]string
	password bool
	err      error
}

func (v *valueFromCheck) Visit(node *ast.Node) {
	if v.err != nil {
		return
	}
	switch n := (*node).(type) {
	case *ast.CallNode:
		id, _ := n.Callee.(*ast.IdentifierNode)
		if id == nil || len(n.Arguments) != 1 {
			return
		}
		switch id.Value {
		case "item":
			name, ok := n.Arguments[0].(*ast.StringNode)
			if !ok {
				v.err = errors.New("item() takes the variable of an earlier question, in quotes")
				return
			}
			if t := v.earlier[name.Value]; t != "multiplechoice" && t != "multiselect" {
				v.err = fmt.Errorf("item(%q) needs an earlier multiplechoice or multiselect question", name.Value)
			}
		case "password":
			if !v.password {
				v.err = errors.New("password() generates the answer of a password question only")
				return
			}
			if k, ok := n.Arguments[0].(*ast.IntegerNode); ok && (k.Value < minGeneratedPassword || k.Value > maxGeneratedPassword) {
				v.err = fmt.Errorf("password(%d): the length must be between %d and %d", k.Value, minGeneratedPassword, maxGeneratedPassword)
			}
		}
	case *ast.MemberNode:
		id, _ := n.Node.(*ast.IdentifierNode)
		name, _ := n.Property.(*ast.StringNode)
		if id == nil || name == nil || id.Value != "answers" {
			return
		}
		switch t, ok := v.earlier[name.Value]; {
		case !ok:
			v.err = fmt.Errorf("answers.%s is not an earlier question", name.Value)
		case t == "password":
			v.err = fmt.Errorf("answers.%s is a password: computed answers don't read passwords", name.Value)
		}
	}
}

// computeAnswer computes a question's answer, typed as its question.
// A nil result leaves the variable unset.
func computeAnswer(qm map[string]any, env *computeEnv) (any, error) {
	prog, err := expr.Compile(str(qm["value_from"]), computeOptions(env)...)
	if err != nil {
		return nil, err
	}
	out, err := vm.Run(prog, env.vars())
	if err != nil || out == nil {
		return nil, err
	}
	switch str(qm["type"]) {
	case "integer":
		n, ok := num(out)
		if !ok || n != math.Trunc(n) {
			return nil, fmt.Errorf("gives %v, not an integer", out)
		}
		return int64(n), nil
	case "float":
		n, ok := num(out)
		if !ok {
			return nil, fmt.Errorf("gives %v, not a number", out)
		}
		return n, nil
	}
	switch v := out.(type) {
	case string:
		return v, nil
	case int, int64, float64, bool:
		return fmt.Sprint(v), nil
	}
	return nil, fmt.Errorf("gives %s, not a text", kindOf(out))
}

// userEnv is a user as expressions see them: id, username, names, email
// and teams (by name).
func userEnv(c *Ctx, id int64) map[string]any {
	env := map[string]any{"id": id, "username": "", "first_name": "", "last_name": "", "email": "", "teams": []any{}}
	if id == 0 {
		return env
	}
	var username, first, last, email string
	var teams []string
	if err := c.DB().QueryRow(c, `SELECT username, first_name, last_name, email,
		ARRAY(SELECT name FROM teams WHERE id IN (SELECT user_teams(u.id)) ORDER BY name)
		FROM users u WHERE id = $1`, id).Scan(&username, &first, &last, &email, &teams); err != nil {
		return env
	}
	env["username"], env["first_name"], env["last_name"], env["email"] = username, first, last, email
	env["teams"] = toAnyList(teams)
	return env
}

// teamEnv is a team as expressions see it, or nil.
func teamEnv(c *Ctx, id any) map[string]any {
	tid, ok := api.ID(id)
	if !ok {
		return nil
	}
	var name string
	if c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", tid).Scan(&name) != nil {
		return nil
	}
	return map[string]any{"id": tid, "name": name}
}

// requestEnv is a portal request as expressions see it: its id, lifetime
// and expiry (RFC 3339, nil for an item that keeps nothing).
func requestEnv(id int64, created time.Time, lifetimeDays any) map[string]any {
	env := map[string]any{"id": id, "lifetime_days": nil, "expires_at": nil}
	if days, ok := api.ID(lifetimeDays); ok {
		env["lifetime_days"] = days
		env["expires_at"] = api.FormatTime(created.Add(time.Duration(days) * 24 * time.Hour))
	}
	return env
}

// loadRequestEnv reads a request's environment and team, for the jobs a
// request's workflow launches.
func loadRequestEnv(c *Ctx, id int64) (request, team map[string]any) {
	var created time.Time
	var lifetime *int64
	var teamID *int64
	if c.DB().QueryRow(c, "SELECT created, lifetime_days, team_id FROM catalog_requests WHERE id = $1", id).Scan(&created, &lifetime, &teamID) != nil {
		return nil, nil
	}
	var days any
	if lifetime != nil {
		days = *lifetime
	}
	if teamID != nil {
		team = teamEnv(c, *teamID)
	}
	return requestEnv(id, created, days), team
}
