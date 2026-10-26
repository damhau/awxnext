package credtypes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Field is a credential type input field.
type Field struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Type         string   `json:"type"`
	Secret       bool     `json:"secret"`
	Multiline    bool     `json:"multiline"`
	Format       string   `json:"format"`
	Choices      []string `json:"choices"`
	HelpText     string   `json:"help_text"`
	AskAtRuntime bool     `json:"ask_at_runtime"`
	Default      any      `json:"default"`
}

// Schema is the parsed "inputs" of a credential type.
type Schema struct {
	Fields       []Field             `json:"fields"`
	Metadata     []Field             `json:"metadata"`
	Required     []string            `json:"required"`
	Dependencies map[string][]string `json:"dependencies"`
}

// Parse converts a stored inputs document into a Schema.
func Parse(inputs any) (*Schema, error) {
	b, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	s := &Schema{}
	if string(b) == "null" || string(b) == "{}" {
		return s, nil
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("invalid inputs schema: %w", err)
	}
	return s, nil
}

func (s *Schema) Field(id string) *Field {
	for i := range s.Fields {
		if s.Fields[i].ID == id {
			return &s.Fields[i]
		}
	}
	return nil
}

func (s *Schema) MetadataField(id string) *Field {
	for i := range s.Metadata {
		if s.Metadata[i].ID == id {
			return &s.Metadata[i]
		}
	}
	return nil
}

// SecretFields returns the ids of secret input fields.
func (s *Schema) SecretFields() map[string]bool {
	out := map[string]bool{}
	for _, f := range s.Fields {
		if f.Secret {
			out[f.ID] = true
		}
	}
	return out
}

func (s *Schema) isRequired(id string) bool {
	for _, r := range s.Required {
		if r == id {
			return true
		}
	}
	return false
}

var (
	idRe     = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	envRe    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	fileRe   = regexp.MustCompile(`^template(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)
	refRe    = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)`)
	sshKeyRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
)

// Environment variables a custom credential type may not set.
var reservedEnv = map[string]bool{
	"PATH": true, "PYTHONPATH": true, "HOME": true, "JOB_ID": true, "INVENTORY_ID": true, "PROJECT_REVISION": true,
	"ANSIBLE_CALLBACK_PLUGINS": true, "ANSIBLE_STDOUT_CALLBACK": true, "ANSIBLE_SSH_CONTROL_PATH_DIR": true,
	"ANSIBLE_COLLECTIONS_PATHS": true, "ANSIBLE_ROLES_PATH": true, "AWX_HOST": true, "MAX_EVENT_RES": true,
	"CALLBACK_QUEUE": true, "CALLBACK_CONNECTION": true, "CACHE": true, "JOB_CALLBACK_DEBUG": true,
	"VIRTUAL_ENV": true, "PROOT_TMP_DIR": true, "ANSIBLE_LIBRARY": true, "INVENTORY_HOSTVARS": true,
}

// ValidateSchema checks a custom credential type's inputs and injectors.
// It returns field errors keyed by "inputs" / "injectors".
func ValidateSchema(inputs, injectors any) map[string][]string {
	errs := map[string][]string{}
	add := func(k, msg string) { errs[k] = append(errs[k], msg) }
	s, err := Parse(inputs)
	if err != nil {
		add("inputs", err.Error())
		return errs
	}
	ids := map[string]bool{}
	for i, f := range s.Fields {
		where := fmt.Sprintf("fields[%d]", i)
		if !idRe.MatchString(f.ID) {
			add("inputs", where+": id must be a valid identifier")
		}
		if ids[f.ID] {
			add("inputs", fmt.Sprintf("%s: duplicate id %q", where, f.ID))
		}
		ids[f.ID] = true
		if f.Label == "" {
			add("inputs", where+": label is required")
		}
		switch f.Type {
		case "", "string", "boolean":
		default:
			add("inputs", fmt.Sprintf("%s: type must be string or boolean, not %q", where, f.Type))
		}
		if f.Format != "" && f.Format != "ssh_private_key" && f.Format != "vault_id" && f.Format != "url" {
			add("inputs", fmt.Sprintf("%s: unsupported format %q", where, f.Format))
		}
	}
	for _, r := range s.Required {
		if !ids[r] {
			add("inputs", fmt.Sprintf("required: %q is not a field", r))
		}
	}
	if injectors == nil {
		return errs
	}
	b, _ := json.Marshal(injectors)
	var inj map[string]map[string]any
	if err := json.Unmarshal(b, &inj); err != nil && string(b) != "null" {
		add("injectors", "injectors must be an object with env, extra_vars and file sections")
		return errs
	}
	files := map[string]bool{}
	for k := range inj["file"] {
		if !fileRe.MatchString(k) {
			add("injectors", fmt.Sprintf("file: invalid key %q (use template or template.<name>)", k))
		}
		files[strings.TrimPrefix(strings.TrimPrefix(k, "template"), ".")] = true
	}
	for section, entries := range inj {
		switch section {
		case "env", "extra_vars", "file":
		default:
			add("injectors", fmt.Sprintf("unknown section %q", section))
			continue
		}
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if section == "env" {
				if !envRe.MatchString(k) {
					add("injectors", fmt.Sprintf("env: invalid variable name %q", k))
				} else if reservedEnv[k] {
					add("injectors", fmt.Sprintf("env: %s cannot be set by a credential", k))
				}
			}
			tmpl, ok := entries[k].(string)
			if !ok {
				if section == "extra_vars" {
					continue // nested structures are allowed in extra_vars
				}
				add("injectors", fmt.Sprintf("%s.%s: must be a string", section, k))
				continue
			}
			for _, m := range refRe.FindAllStringSubmatch(tmpl, -1) {
				ref := m[1]
				switch {
				case ids[ref]:
				case ref == "tower.filename" || ref == "awx.filename":
					if !files[""] {
						add("injectors", fmt.Sprintf("%s.%s: %s requires a file.template injector", section, k, ref))
					}
				case strings.HasPrefix(ref, "tower.filename.") || strings.HasPrefix(ref, "awx.filename."):
					name := ref[strings.LastIndex(ref, ".")+1:]
					if !files[name] {
						add("injectors", fmt.Sprintf("%s.%s: no file.template.%s injector", section, k, name))
					}
				default:
					add("injectors", fmt.Sprintf("%s.%s: unknown variable %q", section, k, ref))
				}
			}
		}
	}
	return errs
}

// ValidateInputs checks credential input values against the schema. Missing
// required fields are allowed (they may be supplied by an input source or at launch).
func ValidateInputs(s *Schema, values map[string]any) map[string][]string {
	errs := map[string][]string{}
	add := func(k, msg string) { errs[k] = append(errs[k], msg) }
	for k, v := range values {
		f := s.Field(k)
		if f == nil {
			add(k, "Additional properties are not allowed ('"+k+"' was unexpected)")
			continue
		}
		if v == nil {
			continue
		}
		switch f.Type {
		case "boolean":
			if _, ok := v.(bool); !ok {
				add(k, "Must be a boolean.")
			}
			continue
		default:
			sv, ok := v.(string)
			if !ok {
				add(k, "Must be a string.")
				continue
			}
			if sv == "ASK" && f.AskAtRuntime {
				continue
			}
			if len(f.Choices) > 0 && sv != "" {
				found := false
				for _, c := range f.Choices {
					if c == sv {
						found = true
					}
				}
				if !found {
					add(k, fmt.Sprintf("%q is not a valid choice (%s).", sv, strings.Join(f.Choices, ", ")))
				}
			}
			if f.Format == "ssh_private_key" && sv != "" && !strings.HasPrefix(sv, "$encrypted$") && !sshKeyRe.MatchString(sv) {
				add(k, "Invalid certificate or key: expected a PEM-encoded private key.")
			}
			if f.Format == "vault_id" && strings.ContainsAny(sv, "@ ") {
				add(k, "Vault IDs cannot contain spaces or @.")
			}
		}
	}
	for id, deps := range s.Dependencies {
		if v, ok := values[id].(string); ok && v != "" {
			for _, d := range deps {
				if dv, ok := values[d].(string); !ok || dv == "" {
					add(id, fmt.Sprintf("cannot be set unless %q is set", d))
				}
			}
		}
	}
	return errs
}

// MissingRequired lists required input fields that have no value.
func MissingRequired(s *Schema, values map[string]any, sourced map[string]bool) []string {
	var out []string
	for _, r := range s.Required {
		if s.Field(r) == nil || sourced[r] {
			continue
		}
		v, ok := values[r]
		if !ok || v == nil || v == "" {
			out = append(out, r)
		}
	}
	return out
}

// ValidateMetadata checks lookup metadata for an external credential type.
func ValidateMetadata(s *Schema, md map[string]any) map[string][]string {
	errs := map[string][]string{}
	for k, v := range md {
		f := s.MetadataField(k)
		if f == nil {
			errs[k] = append(errs[k], "Additional properties are not allowed ('"+k+"' was unexpected)")
			continue
		}
		if _, ok := v.(string); !ok && v != nil {
			errs[k] = append(errs[k], "Must be a string.")
		}
	}
	for _, r := range s.Required {
		if s.MetadataField(r) == nil {
			continue
		}
		if v, _ := md[r].(string); v == "" {
			errs[r] = append(errs[r], "This field is required.")
		}
	}
	return errs
}
