package ansible

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/credtypes"
	"github.com/damhau/awxnext/internal/resources"
)

// Injection is what credentials contribute to a run.
type Injection struct {
	Args      []string
	Env       map[string]string
	ExtraVars map[string]any
	// Passwords maps prompt regexes to answers (ansible-runner env/passwords).
	Passwords map[string]string
	SSHKey    string
}

// MissingPasswordsError lists launch-time passwords that were not supplied.
type MissingPasswordsError struct{ Names []string }

func (e *MissingPasswordsError) Error() string {
	return "Missing passwords needed to start: " + strings.Join(e.Names, ", ")
}

// Inject applies credentials. prompts holds launch-time passwords keyed like
// AWX's credential_passwords (ssh_password, become_password, ssh_key_unlock,
// vault_password, vault_password.<vault id>).
func Inject(r *Run, creds []*resources.ResolvedCredential, prompts map[string]string) (*Injection, error) {
	inj := &Injection{Env: map[string]string{}, ExtraVars: map[string]any{}, Passwords: map[string]string{}}
	var missing []string
	ask := func(rc *resources.ResolvedCredential, field, promptKey string) string {
		v, _ := rc.Inputs[field].(string)
		if v == "ASK" {
			p, ok := prompts[promptKey]
			if !ok || p == "" {
				missing = append(missing, promptKey)
			}
			return p
		}
		return v
	}
	for _, rc := range creds {
		in := func(k string) string { v, _ := rc.Inputs[k].(string); return v }
		switch rc.Kind {
		case "ssh":
			if u := in("username"); u != "" {
				inj.Args = append(inj.Args, "-u", u)
			}
			if pw := ask(rc, "password", "ssh_password"); pw != "" {
				inj.Args = append(inj.Args, "--ask-pass")
				inj.Passwords[`SSH password:\s*?$`] = pw
				inj.Passwords[`Password:\s*?$`] = pw
			}
			if key := in("ssh_key_data"); key != "" {
				inj.SSHKey = strings.TrimSpace(key) + "\n"
				if unlock := ask(rc, "ssh_key_unlock", "ssh_key_unlock"); unlock != "" {
					inj.Passwords[`Enter passphrase for .*:\s*?$`] = unlock
					inj.Passwords[`Bad passphrase, try again for .*:\s*?$`] = ""
				}
			}
			if m := in("become_method"); m != "" {
				inj.Args = append(inj.Args, "--become-method", m)
			}
			if u := in("become_username"); u != "" {
				inj.Args = append(inj.Args, "--become-user", u)
			}
			if pw := ask(rc, "become_password", "become_password"); pw != "" {
				inj.Args = append(inj.Args, "--ask-become-pass")
				for _, p := range []string{`BECOME password.*:\s*?$`, `[Ss][Uu][Dd][Oo] password.*:\s*?$`, `SU password.*:\s*?$`, `DZDO password.*:\s*?$`, `PBRUN password.*:\s*?$`, `PFEXEC password.*:\s*?$`, `RUNAS password.*:\s*?$`, `DOAS password.*:\s*?$`, `KSU password.*:\s*?$`, `MACHINECTL password.*:\s*?$`, `SESU password.*:\s*?$`} {
					inj.Passwords[p] = pw
				}
			}
		case "vault":
			id := in("vault_id")
			key := "vault_password"
			if id != "" {
				key = "vault_password." + id
			}
			pw, _ := rc.Inputs["vault_password"].(string)
			if pw == "ASK" {
				pw = prompts[key]
				if pw == "" && id != "" {
					pw = prompts["vault_password"]
				}
				if pw == "" {
					missing = append(missing, key)
				}
			}
			if id != "" {
				inj.Args = append(inj.Args, "--vault-id", id+"@prompt")
				inj.Passwords[`Vault password \(`+regexp.QuoteMeta(id)+`\):\s*?$`] = pw
			} else {
				inj.Args = append(inj.Args, "--ask-vault-pass")
				inj.Passwords[`Vault password:\s*?$`] = pw
			}
		case "net":
			inj.Env["ANSIBLE_NET_USERNAME"] = in("username")
			inj.Env["ANSIBLE_NET_PASSWORD"] = in("password")
			if key := in("ssh_key_data"); key != "" {
				name := fmt.Sprintf("env/awx_net_key_%d", rc.ID)
				if err := r.WriteFile(name, []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
					return nil, err
				}
				inj.Env["ANSIBLE_NET_SSH_KEYFILE"] = r.Path(name)
			}
			if auth, _ := rc.Inputs["authorize"].(bool); auth {
				inj.Env["ANSIBLE_NET_AUTHORIZE"] = "1"
				inj.Env["ANSIBLE_NET_AUTH_PASS"] = in("authorize_password")
			} else {
				inj.Env["ANSIBLE_NET_AUTHORIZE"] = "0"
			}
		case "scm", "registry", "galaxy", "token", "cryptography", "external", "insights":
			// Used by project sync, image pulls, galaxy installs or webhooks; nothing to inject.
		default:
			if err := applyInjectors(r, rc, inj); err != nil {
				return nil, fmt.Errorf("credential %q: %w", rc.Name, err)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &MissingPasswordsError{Names: missing}
	}
	return inj, nil
}

// applyInjectors renders a credential type's env, extra_vars and file injectors.
func applyInjectors(r *Run, rc *resources.ResolvedCredential, inj *Injection) error {
	injectors := rc.Injectors
	if injectors == nil {
		return nil
	}
	vars := map[string]any{}
	for _, f := range rc.Schema.Fields {
		if v, ok := rc.Inputs[f.ID]; ok {
			vars[f.ID] = v
		} else if f.Default != nil {
			vars[f.ID] = f.Default
		} else if f.Type == "boolean" {
			vars[f.ID] = false
		} else {
			vars[f.ID] = ""
		}
	}
	files := map[string]string{}
	if fm, ok := injectors["file"].(map[string]any); ok {
		keys := make([]string, 0, len(fm))
		for k := range fm {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			tmpl, _ := fm[k].(string)
			name := strings.TrimPrefix(strings.TrimPrefix(k, "template"), ".")
			content, err := credtypes.Render(tmpl, vars, files)
			if err != nil {
				return err
			}
			rel := fmt.Sprintf("env/awx_cred_%d_%s", rc.ID, name)
			if name == "" {
				rel = fmt.Sprintf("env/awx_cred_%d", rc.ID)
			}
			if err := r.WriteFile(rel, []byte(content), 0o600); err != nil {
				return err
			}
			files[name] = r.Path(rel)
		}
	}
	if em, ok := injectors["env"].(map[string]any); ok {
		for k, v := range em {
			tmpl, _ := v.(string)
			val, err := credtypes.Render(tmpl, vars, files)
			if err != nil {
				return err
			}
			inj.Env[k] = val
		}
	}
	if xm, ok := injectors["extra_vars"].(map[string]any); ok {
		for k, v := range xm {
			rendered, err := renderAny(v, vars, files)
			if err != nil {
				return err
			}
			inj.ExtraVars[k] = rendered
		}
	}
	return nil
}

func renderAny(v any, vars map[string]any, files map[string]string) (any, error) {
	switch x := v.(type) {
	case string:
		return credtypes.Render(x, vars, files)
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			r, err := renderAny(e, vars, files)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := renderAny(e, vars, files)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// ShellQuote joins args for ansible-runner's env/cmdline (parsed with shlex).
func ShellQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~!") {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'"'"'`) + "'"
		}
	}
	return strings.Join(out, " ")
}
