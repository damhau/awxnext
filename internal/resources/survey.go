package resources

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
)

var surveyTypes = map[string]bool{"text": true, "textarea": true, "password": true, "integer": true, "float": true, "multiplechoice": true, "multiselect": true}

var varNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// surveyChoices accepts a list or a newline-separated string.
func surveyChoices(v any) []string {
	switch x := v.(type) {
	case []any:
		var out []string
		for _, c := range x {
			out = append(out, fmt.Sprint(c))
		}
		return out
	case string:
		var out []string
		for _, line := range strings.Split(x, "\n") {
			if s := strings.TrimSpace(line); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// ValidateSurveySpec checks a survey spec and encrypts password defaults,
// keeping previously stored defaults sent back as $encrypted$.
func ValidateSurveySpec(c *Ctx, raw any, previous map[string]any) (map[string]any, error) {
	bad := func(msg string) error { return api.Detail(400, msg) }
	spec, ok := raw.(map[string]any)
	if !ok {
		return nil, bad("Survey spec must be an object.")
	}
	if len(spec) == 0 {
		return spec, nil
	}
	if _, ok := spec["name"].(string); !ok {
		return nil, bad("'name' missing from survey spec.")
	}
	if _, ok := spec["description"].(string); !ok {
		return nil, bad("'description' missing from survey spec.")
	}
	qs, ok := spec["spec"].([]any)
	if !ok {
		return nil, bad("'spec' must be a list of items.")
	}
	prevDefaults := map[string]string{}
	if pq, ok := previous["spec"].([]any); ok {
		for _, q := range pq {
			if qm, ok := q.(map[string]any); ok && qm["type"] == "password" {
				prevDefaults[str(qm["variable"])] = str(qm["default"])
			}
		}
	}
	seen := map[string]bool{}
	for i, q := range qs {
		qm, ok := q.(map[string]any)
		if !ok {
			return nil, bad(fmt.Sprintf("Survey question %d is not a json object.", i))
		}
		typ := str(qm["type"])
		if !surveyTypes[typ] {
			return nil, bad(fmt.Sprintf("'%s' in survey question %d is not one of '%s' allowed question types.", typ, i, "text, textarea, password, integer, float, multiplechoice, multiselect"))
		}
		if str(qm["question_name"]) == "" {
			return nil, bad(fmt.Sprintf("'question_name' missing from survey question %d.", i))
		}
		v := str(qm["variable"])
		if !varNameRe.MatchString(v) {
			return nil, bad(fmt.Sprintf("'variable' '%s' in survey question %d is not a valid variable name.", v, i))
		}
		if seen[v] {
			return nil, bad(fmt.Sprintf("'variable' '%s' duplicated in survey question %d.", v, i))
		}
		if isAnsibleVar(v) && !c.App.Settings.Bool(c, "ALLOW_ANSIBLE_VARS_AT_LAUNCH") {
			return nil, bad(fmt.Sprintf("'variable' '%s' in survey question %d: ansible_* variables control how Ansible connects and runs, so whoever answers could choose them. Set them on the template, inventory or host instead.", v, i))
		}
		seen[v] = true
		if _, ok := qm["required"].(bool); !ok {
			return nil, bad(fmt.Sprintf("'required' missing from survey question %d.", i))
		}
		def, hasDef := qm["default"]
		if def == "" {
			hasDef = false
		}
		dynamic := qm["choices_from"] != nil
		if dynamic {
			src, ok := qm["choices_from"].(map[string]any)
			if !ok || (typ != "multiplechoice" && typ != "multiselect") {
				return nil, bad(fmt.Sprintf("'choices_from' in survey question %d needs a multiplechoice or multiselect question.", i))
			}
			if err := validateChoicesFrom(src); err != nil {
				return nil, bad(fmt.Sprintf("%v in survey question %d.", err, i))
			}
		}
		if si := qm["show_if"]; si != nil {
			m, _ := si.(map[string]any)
			if dep := str(m["variable"]); !seen[dep] || dep == v {
				return nil, bad(fmt.Sprintf("'show_if' in survey question %d must name the variable of an earlier question.", i))
			}
			if values, ok := m["values"].([]any); !ok || len(values) == 0 {
				return nil, bad(fmt.Sprintf("'show_if' in survey question %d needs a list of values.", i))
			}
		}
		if p := qm["pattern"]; p != nil && p != "" {
			ps, ok := p.(string)
			if !ok || (typ != "text" && typ != "textarea" && typ != "password") {
				return nil, bad(fmt.Sprintf("'pattern' in survey question %d needs a text, textarea or password question.", i))
			}
			re, err := surveyPattern(ps)
			if err != nil {
				return nil, bad(fmt.Sprintf("'pattern' in survey question %d is not a valid regular expression: %v", i, err))
			}
			if s, ok := def.(string); ok && hasDef && typ != "password" && !re.MatchString(s) {
				return nil, bad(fmt.Sprintf("Default value in survey question %d does not match its pattern.", i))
			}
		}
		switch typ {
		case "multiplechoice", "multiselect":
			choices := surveyChoices(qm["choices"])
			if len(choices) == 0 && !dynamic {
				return nil, bad(fmt.Sprintf("Survey question %d of type %s must provide 'choices'.", i, typ))
			}
			if hasDef && !dynamic {
				var defs []string
				if typ == "multiselect" {
					defs = surveyChoices(def)
				} else {
					defs = []string{fmt.Sprint(def)}
				}
				for _, d := range defs {
					if !contains(choices, d) {
						return nil, bad(fmt.Sprintf("Default choice must be answered from the choices listed in survey question %d.", i))
					}
				}
			}
		case "integer":
			if hasDef {
				n, ok := num(def)
				if !ok || n != math.Trunc(n) {
					return nil, bad(fmt.Sprintf("Default value %v in survey question %d expected to be int.", def, i))
				}
			}
		case "float":
			if hasDef {
				if _, ok := num(def); !ok {
					return nil, bad(fmt.Sprintf("Default value %v in survey question %d expected to be float.", def, i))
				}
			}
		case "password":
			if s, _ := def.(string); s == crypto.Placeholder {
				if prev, ok := prevDefaults[v]; ok {
					qm["default"] = prev
				} else {
					return nil, bad(fmt.Sprintf("$encrypted$ is a reserved keyword for password question defaults, survey question %d is type password.", i))
				}
			} else if s != "" && !crypto.IsEncrypted(s) {
				enc, err := c.App.Crypto.Encrypt(s)
				if err != nil {
					return nil, err
				}
				qm["default"] = enc
			}
		default:
			if hasDef {
				if _, ok := def.(string); !ok {
					return nil, bad(fmt.Sprintf("Default value %v in survey question %d expected to be string.", def, i))
				}
			}
		}
	}
	return spec, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// surveyPattern compiles a question's pattern, which must match the whole answer.
func surveyPattern(p string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + p + ")$")
}

// shown reports whether a question's show_if holds for the answers so far:
// the answer to its variable (or, for a multiselect, any of its answers) is
// one of its values.
func shown(q map[string]any, answers map[string]any) bool {
	si, ok := q["show_if"].(map[string]any)
	if !ok {
		return true
	}
	ans, ok := answers[str(si["variable"])]
	if !ok {
		return false
	}
	values, _ := si["values"].([]any)
	given := []any{ans}
	if list, ok := ans.([]any); ok {
		given = list
	}
	for _, g := range given {
		for _, want := range values {
			if fmt.Sprint(g) == fmt.Sprint(want) {
				return true
			}
		}
	}
	return false
}

// ApplySurvey validates answers and fills defaults. It returns the survey
// variables (password answers encrypted) and the names of password variables.
// Questions hidden by show_if are skipped and their answers dropped; dynamic
// choices are resolved against sc as the user of c.
func ApplySurvey(c *Ctx, spec map[string]any, answers map[string]any, sc SurveyContext) (map[string]any, []string, []string) {
	out := map[string]any{}
	answered := map[string]any{} // valid answers so far, for show_if
	var passwords, errs []string
	qs, _ := spec["spec"].([]any)
	for _, q := range qs {
		qm, ok := q.(map[string]any)
		if !ok {
			continue
		}
		v := str(qm["variable"])
		typ := str(qm["type"])
		if !shown(qm, answered) {
			continue
		}
		required, _ := qm["required"].(bool)
		ans, given := answers[v]
		if typ == "password" {
			passwords = append(passwords, v)
			if s, _ := ans.(string); s == crypto.Placeholder {
				given = false
			}
		}
		if !given || ans == nil || (ans == "" && typ != "text" && typ != "textarea" && typ != "password") {
			def, hasDef := qm["default"]
			if hasDef && def != "" && def != nil {
				ans, given = def, true
				if typ == "password" {
					out[v] = def // already encrypted
					continue
				}
				if typ == "multiselect" {
					ans = toAnyList(surveyChoices(def))
				}
			} else if required {
				errs = append(errs, fmt.Sprintf("'%s' value missing", v))
				continue
			} else {
				continue
			}
		}
		minV, hasMin := num(qm["min"])
		maxV, hasMax := num(qm["max"])
		switch typ {
		case "text", "textarea", "password":
			s, ok := ans.(string)
			if !ok {
				errs = append(errs, fmt.Sprintf("Value %v for '%s' expected to be a string.", ans, v))
				continue
			}
			if required && s == "" {
				errs = append(errs, fmt.Sprintf("'%s' value missing", v))
				continue
			}
			if hasMin && float64(len(s)) < minV && s != "" {
				errs = append(errs, fmt.Sprintf("'%s' value %q is too small (length is %d must be at least %v).", v, s, len(s), minV))
			}
			if hasMax && maxV > 0 && float64(len(s)) > maxV {
				errs = append(errs, fmt.Sprintf("'%s' value is too large (must be no more than %v).", v, maxV))
			}
			if pat := str(qm["pattern"]); pat != "" && s != "" && !(typ == "password" && crypto.IsEncrypted(s)) {
				if re, err := surveyPattern(pat); err == nil && !re.MatchString(s) {
					msg := fmt.Sprintf("'%s' value does not match the expected format.", v)
					if hint := str(qm["pattern_hint"]); hint != "" {
						msg = fmt.Sprintf("'%s': %s", v, hint)
					}
					errs = append(errs, msg)
					continue
				}
			}
			answered[v] = s
			if typ == "password" && s != "" {
				enc, err := c.App.Crypto.Encrypt(s)
				if err == nil {
					out[v] = enc
				}
				continue
			}
			out[v] = s
		case "integer":
			n, ok := num(ans)
			if s, isStr := ans.(string); isStr {
				var x json.Number = json.Number(s)
				n, ok = num(x)
			}
			if !ok || n != math.Trunc(n) {
				errs = append(errs, fmt.Sprintf("Value %v for '%s' expected to be an integer.", ans, v))
				continue
			}
			if hasMin && n < minV || hasMax && n > maxV {
				errs = append(errs, fmt.Sprintf("'%s' value %v must be between %v and %v.", v, n, minV, maxV))
				continue
			}
			out[v], answered[v] = int64(n), int64(n)
		case "float":
			n, ok := num(ans)
			if !ok {
				errs = append(errs, fmt.Sprintf("Value %v for '%s' expected to be a numeric type.", ans, v))
				continue
			}
			if hasMin && n < minV || hasMax && n > maxV {
				errs = append(errs, fmt.Sprintf("'%s' value %v must be between %v and %v.", v, n, minV, maxV))
				continue
			}
			out[v], answered[v] = n, n
		case "multiplechoice":
			choices, err := resolveChoices(c, qm, sc)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Could not list the choices for '%s': %v", v, err))
				continue
			}
			s := fmt.Sprint(ans)
			if !contains(choices, s) {
				errs = append(errs, fmt.Sprintf("Value %s for '%s' expected to be one of %v.", s, v, choices))
				continue
			}
			out[v], answered[v] = s, s
		case "multiselect":
			list, ok := ans.([]any)
			if !ok {
				errs = append(errs, fmt.Sprintf("'%s' value is expected to be a list.", v))
				continue
			}
			choices, err := resolveChoices(c, qm, sc)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Could not list the choices for '%s': %v", v, err))
				continue
			}
			for _, x := range list {
				if !contains(choices, fmt.Sprint(x)) {
					errs = append(errs, fmt.Sprintf("Value %v for '%s' expected to be one of %v.", x, v, choices))
				}
			}
			out[v], answered[v] = list, list
		}
	}
	return out, passwords, errs
}

func toAnyList(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// surveyVariables returns the variable names defined by a survey.
func surveyVariables(spec map[string]any) map[string]bool {
	out := map[string]bool{}
	qs, _ := spec["spec"].([]any)
	for _, q := range qs {
		if qm, ok := q.(map[string]any); ok {
			out[str(qm["variable"])] = true
		}
	}
	return out
}
