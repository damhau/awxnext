package resources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/credplugins"
	"github.com/damhau/awxnext/internal/credtypes"
	"github.com/damhau/awxnext/internal/crypto"
)

var credentialKinds = labelled(
	"ssh", "Machine", "vault", "Vault", "net", "Network", "scm", "Source Control", "cloud", "Cloud",
	"registry", "Container Registry", "token", "Personal Access Token", "insights", "Insights",
	"external", "External", "kubernetes", "Kubernetes", "galaxy", "Galaxy/Automation Hub", "cryptography", "Cryptography",
)

// SeedCredentialTypes upserts the managed credential types.
func SeedCredentialTypes(ctx context.Context, app *api.App) error {
	for _, d := range credtypes.Managed {
		injectors := d.Injectors
		if injectors == nil {
			injectors = map[string]any{}
		}
		_, err := app.DB.Exec(ctx, `INSERT INTO credential_types (name, description, kind, namespace, managed, inputs, injectors)
			VALUES ($1, $2, $3, $4, true, $5, $6)
			ON CONFLICT (name, kind) DO UPDATE SET namespace = EXCLUDED.namespace, managed = true,
				inputs = EXCLUDED.inputs, injectors = EXCLUDED.injectors, description = EXCLUDED.description`,
			d.Name, d.Description, d.Kind, d.Namespace, d.Inputs, injectors)
		if err != nil {
			return fmt.Errorf("credential type %s: %w", d.Name, err)
		}
	}
	return nil
}

// credentialType loads the schema, kind and namespace of a credential type.
func credentialType(c *Ctx, id int64) (*credtypes.Schema, string, string, error) {
	var inputs map[string]any
	var kind string
	var ns *string
	if err := c.DB().QueryRow(c, "SELECT inputs, kind, namespace FROM credential_types WHERE id = $1", id).Scan(&inputs, &kind, &ns); err != nil {
		return nil, "", "", api.FieldError("credential_type", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	s, err := credtypes.Parse(inputs)
	namespace := ""
	if ns != nil {
		namespace = *ns
	}
	return s, kind, namespace, err
}

func inputErrors(errs map[string][]string) error {
	if len(errs) == 0 {
		return nil
	}
	return &api.Error{Status: 400, Body: Obj{"inputs": errs}}
}

// redactInputs replaces stored secret values with $encrypted$.
func redactInputs(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, x := range m {
		if s, ok := x.(string); ok && crypto.IsEncrypted(s) {
			out[k] = crypto.Placeholder
		} else {
			out[k] = x
		}
	}
	return out
}

// ResolvedCredential is a credential with decrypted inputs, ready for injection.
type ResolvedCredential struct {
	ID        int64
	Name      string
	Kind      string
	Namespace string
	TypeID    int64
	Inputs    map[string]any
	Injectors map[string]any
	Schema    *credtypes.Schema
}

// ResolveCredential decrypts a credential's inputs and fills fields linked to
// external secret stores. Values still set to "ASK" must be provided at launch.
func ResolveCredential(ctx context.Context, app *api.App, id int64) (*ResolvedCredential, error) {
	rc := &ResolvedCredential{ID: id}
	var inputs, schema map[string]any
	var ns *string
	err := app.DB.QueryRow(ctx, `SELECT c.name, c.inputs, ct.id, ct.kind, ct.namespace, ct.inputs, ct.injectors
		FROM credentials c JOIN credential_types ct ON ct.id = c.credential_type_id WHERE c.id = $1`, id).
		Scan(&rc.Name, &inputs, &rc.TypeID, &rc.Kind, &ns, &schema, &rc.Injectors)
	if err != nil {
		return nil, fmt.Errorf("credential %d: %w", id, err)
	}
	if ns != nil {
		rc.Namespace = *ns
	}
	if rc.Schema, err = credtypes.Parse(schema); err != nil {
		return nil, err
	}
	rc.Inputs = map[string]any{}
	for k, v := range inputs {
		if s, ok := v.(string); ok && crypto.IsEncrypted(s) {
			plain, err := app.Crypto.Decrypt(s)
			if err != nil {
				return nil, fmt.Errorf("credential %q: decrypt %s: %w", rc.Name, k, err)
			}
			v = plain
		}
		rc.Inputs[k] = v
	}
	rows, err := app.DB.Query(ctx, `SELECT input_field_name, source_credential_id, metadata FROM credential_input_sources WHERE target_credential_id = $1`, id)
	if err != nil {
		return nil, err
	}
	type src struct {
		field string
		id    int64
		md    map[string]any
	}
	var sources []src
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.field, &s.id, &s.md); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, s)
	}
	rows.Close()
	for _, s := range sources {
		source, err := ResolveCredential(ctx, app, s.id)
		if err != nil {
			return nil, err
		}
		v, err := credplugins.Lookup(ctx, source.Namespace, source.Inputs, s.md)
		if err != nil {
			return nil, fmt.Errorf("credential %q: lookup of %s from %q failed: %w", rc.Name, s.field, source.Name, err)
		}
		rc.Inputs[s.field] = v
	}
	return rc, nil
}

func registerCredentials() {
	ctypes := api.Register(&Resource{
		Name:     "credential_types",
		Type:     "credential_type",
		Table:    "credential_types",
		Verbose:  "Credential Type",
		Ordering: "name",
		Fields: []*Field{
			{Name: "kind", Type: api.TChoice, Required: true, Choices: credentialKinds, Help: "The credential type kind. Custom credential types must be cloud or net."},
			{Name: "namespace", Type: api.TString, ReadOnly: true, Nullable: true},
			{Name: "managed", Type: api.TBool, ReadOnly: true},
			{Name: "inputs", Type: api.TJSON, Default: Obj{}, Help: "Enter inputs using either JSON or YAML syntax. Refer to the documentation for example syntax."},
			{Name: "injectors", Type: api.TJSON, Default: Obj{}, Help: "Enter injectors using either JSON or YAML syntax. Refer to the documentation for example syntax."},
		},
		Versioned: true,
		CanAdd: func(c *Ctx, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if k := str(data["kind"]); k != "cloud" && k != "net" {
				return api.FieldError("kind", "Must be 'cloud' or 'net', not "+k)
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if boolv(old["managed"]) {
				return api.Detail(403, "Managed credential types cannot be modified.")
			}
			if k := str(data["kind"]); k != "cloud" && k != "net" {
				return api.FieldError("kind", "Must be 'cloud' or 'net', not "+k)
			}
			if countQuery(c, "SELECT count(*) FROM credentials WHERE credential_type_id = $1", old["id"]) > 0 &&
				fmt.Sprint(normalize(old["inputs"])) != fmt.Sprint(normalize(data["inputs"])) {
				return api.Detail(403, "Modifications to inputs are not allowed for credential types that are in use.")
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if boolv(obj["managed"]) {
				return api.Detail(403, "Managed credential types cannot be deleted.")
			}
			if countQuery(c, "SELECT count(*) FROM credentials WHERE credential_type_id = $1", obj["id"]) > 0 {
				return api.Detail(403, "Credential types that are in use cannot be deleted.")
			}
			return nil
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			inputs, injectors := data["inputs"], data["injectors"]
			if old != nil {
				if _, ok := data["inputs"]; !ok {
					inputs = old["inputs"]
				}
				if _, ok := data["injectors"]; !ok {
					injectors = old["injectors"]
				}
			}
			errs := credtypes.ValidateSchema(normalize(inputs), normalize(injectors))
			if len(errs) > 0 {
				body := Obj{}
				for k, v := range errs {
					body[k] = v
				}
				return &api.Error{Status: 400, Body: body}
			}
			return nil
		},
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			ok := c.User.Superuser && !boolv(obj["managed"])
			return map[string]bool{"edit": ok, "delete": ok}
		},
	})
	ctypes.SubLists = []*SubList{readOnlyChildSub("credentials", "credentials", "credential_type_id")}
	ctypes.Actions = []*api.Action{{Name: "test", Methods: []string{"POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		data, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		ns := str(obj["namespace"])
		if str(obj["kind"]) != "external" {
			api.WriteError(w, api.BadRequest("Only external credential types can be tested."))
			return
		}
		inputs, _ := normalize(data["inputs"]).(map[string]any)
		md, _ := normalize(data["metadata"]).(map[string]any)
		testLookup(c, w, ns, inputs, md)
	}}}

	creds := api.Register(&Resource{
		Name:         "credentials",
		Type:         "credential",
		Table:        "credentials",
		Verbose:      "Credential",
		Ordering:     "name",
		ContentType:  "credential",
		OrgColumn:    "organization_id",
		FolderColumn: "folder_id",
		Versioned:    true,
		Managed:      true,
		Copyable:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Nullable: true, Help: "Inherit permissions from organization roles. If provided on creation, do not give either user or team."},
			{Name: "credential_type", Type: api.TFK, Ref: "credential_types", Required: true, Help: "Specify the type of credential you want to create. Refer to the documentation for details on each type."},
			{Name: "inputs", Type: api.TJSON, Default: Obj{}, Help: "Enter inputs using either JSON or YAML syntax. Refer to the documentation for example syntax.", Redact: redactInputs},
			folderField(),
			{Name: "managed", Type: api.TBool, ReadOnly: true},
			{Name: "user", Type: api.TFK, Ref: "users", Virtual: true, WriteOnly: true, Help: "Write-only field used to add a user to the owner role. If provided, do not give either team or organization. Only valid for creation."},
			{Name: "team", Type: api.TFK, Ref: "teams", Virtual: true, WriteOnly: true, Help: "Write-only field used to add a team to the owner role. If provided, do not give either user or organization. Only valid for creation."},
		},
		// Managed credentials (the public Galaxy one) are read-only, as in AWX.
		CanChange: func(c *Ctx, old, data Obj) error {
			if boolv(old["managed"]) {
				return api.Detail(403, "Modifications not allowed for managed credentials")
			}
			return requirePerm(c, "credentials", old, "change")
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if boolv(obj["managed"]) {
				return api.Detail(403, "Deletion not allowed for managed credentials")
			}
			return requirePerm(c, "credentials", obj, "delete")
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if uid, ok := api.ID(data["user"]); ok && uid != c.User.UserID && !c.User.Superuser {
				return api.Detail(403, "You can only create personal credentials for yourself.")
			}
			if tid, ok := api.ID(data["team"]); ok && !c.User.Superuser {
				ok, err := api.HasPermID(c, "teams", tid, "change")
				if err != nil || !ok {
					return api.ErrForbidden
				}
			}
			if _, ok := api.ID(data["organization"]); !ok {
				if c.User.Anonymous() {
					return api.ErrForbidden
				}
				return nil // personal credential
			}
			ok, err := api.CanAddIn(c, api.Lookup("credentials"), data)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			typeID, _ := api.ID(data["credential_type"])
			if old != nil {
				if _, ok := data["credential_type"]; !ok {
					typeID, _ = api.ID(old["credential_type"])
				} else if oldType, _ := api.ID(old["credential_type"]); oldType != typeID {
					if countQuery(c, `SELECT (SELECT count(*) FROM unified_job_template_credentials WHERE credential_id = $1)
						+ (SELECT count(*) FROM unified_job_templates WHERE credential_id = $1)`, old["id"]) > 0 {
						return api.FieldError("credential_type", "You cannot change the credential type of the credential, as it may break the functionality of the resources using it.")
					}
				}
			}
			if tid, ok := api.ID(data["team"]); ok {
				var torg int64
				c.DB().QueryRow(c, "SELECT organization_id FROM teams WHERE id = $1", tid).Scan(&torg)
				if org, ok := api.ID(data["organization"]); ok && org != torg {
					return api.FieldError("team", "Credential organization must be set and match the team's organization.")
				}
			}
			raw, given := data["inputs"]
			if !given && old != nil {
				return nil
			}
			schema, _, namespace, err := credentialType(c, typeID)
			if err != nil {
				return err
			}
			inputs, _ := normalize(raw).(map[string]any)
			if inputs == nil {
				inputs = map[string]any{}
			}
			var oldInputs map[string]any
			if old != nil {
				oldInputs, _ = old["inputs"].(map[string]any)
			}
			// $encrypted$ keeps the stored secret.
			for k, v := range inputs {
				if v == crypto.Placeholder {
					if prev, ok := oldInputs[k]; ok {
						inputs[k] = prev
					} else {
						delete(inputs, k)
					}
				}
			}
			if err := inputErrors(credtypes.ValidateInputs(schema, inputs)); err != nil {
				return err
			}
			// As AWX checks its managed types: an encrypted key needs its
			// passphrase, and a passphrase needs an encrypted key.
			if namespace != "" && schema.Field("ssh_key_unlock") != nil {
				key := str(inputs["ssh_key_data"])
				if crypto.IsEncrypted(key) {
					key, _ = c.App.Crypto.Decrypt(key)
				}
				unlock := str(inputs["ssh_key_unlock"])
				switch encrypted := credtypes.KeyEncrypted(key); {
				case encrypted && unlock == "":
					return inputErrors(map[string][]string{"ssh_key_unlock": {"must be set when SSH key is encrypted."}})
				case !encrypted && key != "" && unlock != "":
					return inputErrors(map[string][]string{"ssh_key_unlock": {"should not be set when SSH key is not encrypted."}})
				}
			}
			secrets := schema.SecretFields()
			for k, v := range inputs {
				if s, ok := v.(string); ok && secrets[k] && s != "" && s != "ASK" {
					enc, err := c.App.Crypto.Encrypt(s)
					if err != nil {
						return err
					}
					inputs[k] = enc
				}
			}
			data["inputs"] = inputs
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			if uid, ok := api.ID(c.Extra["credential_user"]); ok {
				if err := Assign(c, "Credential Admin", uid, 0, "credential", id); err != nil {
					return err
				}
			}
			if tid, ok := api.ID(c.Extra["credential_team"]); ok {
				if err := Assign(c, "Credential Use", 0, tid, "credential", id); err != nil {
					return err
				}
			}
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			o["inputs"] = redactInputs(o["inputs"])
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				id, _ := api.ID(o["id"])
				typeID, _ := api.ID(o["credential_type"])
				var kind string
				var ns *string
				c.DB().QueryRow(c, "SELECT kind, namespace FROM credential_types WHERE id = $1", typeID).Scan(&kind, &ns)
				o["kind"] = kind
				if ns != nil {
					o["kind"] = *ns
				}
				o["cloud"] = kind == "cloud"
				o["kubernetes"] = kind == "kubernetes"
				owners := []Obj{}
				rows, err := c.DB().Query(c, `SELECT 'user', u.id, u.username, '' FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
					JOIN users u ON u.id = ra.user_id WHERE ra.content_type = 'credential' AND ra.object_id = $1 AND rd.name = 'Credential Admin'
					UNION ALL SELECT 'team', t.id, t.name, t.description FROM role_assignments ra JOIN teams t ON t.id = ra.team_id
					WHERE ra.content_type = 'credential' AND ra.object_id = $1
					UNION ALL SELECT 'organization', o.id, o.name, o.description FROM credentials cr JOIN organizations o ON o.id = cr.organization_id WHERE cr.id = $1`, id)
				if err != nil {
					return err
				}
				for rows.Next() {
					var typ, name, desc string
					var oid int64
					rows.Scan(&typ, &oid, &name, &desc)
					res := api.LookupType(typ)
					owners = append(owners, Obj{"id": oid, "type": typ, "name": name, "description": desc, "url": res.URL(oid)})
				}
				rows.Close()
				o["summary_fields"].(Obj)["owners"] = owners
			}
			return nil
		},
		AfterCopy: func(c *Ctx, src, dst Obj) error {
			_, err := c.DB().Exec(c, `INSERT INTO credential_input_sources (target_credential_id, source_credential_id, input_field_name, metadata, description)
				SELECT $2, source_credential_id, input_field_name, metadata, description FROM credential_input_sources WHERE target_credential_id = $1`,
				src["id"], dst["id"])
			return err
		},
	})
	// Virtual owner fields are only meaningful on create: stash them for AfterCreate.
	prevBefore := creds.BeforeSave
	creds.BeforeSave = func(c *Ctx, old, data Obj) error {
		if old == nil {
			c.Extra["credential_user"] = data["user"]
			c.Extra["credential_team"] = data["team"]
		}
		return prevBefore(c, old, data)
	}
	creds.SubLists = []*SubList{
		childSub("input_sources", "credential_input_sources", "target_credential_id", "target_credential"),
		roleUserSub("owner_users", "Credential Admin", "credential"),
		{
			Name:   "owner_teams",
			Target: "teams",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT team_id FROM role_assignments WHERE content_type = 'credential' AND object_id = %s)", arg(pid))
			},
			ReadOnly: true,
		},
	}
	creds.Actions = []*api.Action{{Name: "test", Methods: []string{"POST"}, Perm: "use", Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		data, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		id, _ := api.ID(obj["id"])
		rc, err := ResolveCredential(c, c.App, id)
		if err != nil {
			api.WriteError(w, api.Detail(400, err.Error()))
			return
		}
		if rc.Kind != "external" {
			api.WriteError(w, api.BadRequest("Only external credentials can be tested."))
			return
		}
		// Inputs in the request override stored ones (for testing edits before saving).
		if in, ok := normalize(data["inputs"]).(map[string]any); ok {
			for k, v := range in {
				if v != crypto.Placeholder {
					rc.Inputs[k] = v
				}
			}
		}
		md, _ := normalize(data["metadata"]).(map[string]any)
		testLookup(c, w, rc.Namespace, rc.Inputs, md)
	}}}

	api.Register(&Resource{
		Name:    "credential_input_sources",
		Type:    "credential_input_source",
		Table:   "credential_input_sources",
		Verbose: "Credential Input Source",
		NoName:  true,
		Fields: []*Field{
			{Name: "description", Type: api.TText, Default: ""},
			{Name: "input_field_name", Type: api.TString, Required: true, MaxLength: 1024},
			{Name: "metadata", Type: api.TJSON, Default: Obj{}},
			{Name: "target_credential", Type: api.TFK, Ref: "credentials", Required: true},
			{Name: "source_credential", Type: api.TFK, Ref: "credentials", Required: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			creds := api.Lookup("credentials")
			return fmt.Sprintf("t.target_credential_id IN (SELECT t.id FROM credentials t WHERE %s)", viewOf(c, creds, arg))
		},
		CanAdd:    checkInputSource,
		CanChange: func(c *Ctx, old, data Obj) error { return checkInputSource(c, data) },
		CanDelete: func(c *Ctx, obj Obj) error {
			return api.RequirePermID(c, "target_credential", "credentials", obj["target_credential"], "change")
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			merged := Obj{}
			for k, v := range old {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			srcID, _ := api.ID(merged["source_credential"])
			tgtID, _ := api.ID(merged["target_credential"])
			var srcType, tgtType int64
			c.DB().QueryRow(c, "SELECT credential_type_id FROM credentials WHERE id = $1", srcID).Scan(&srcType)
			c.DB().QueryRow(c, "SELECT credential_type_id FROM credentials WHERE id = $1", tgtID).Scan(&tgtType)
			srcSchema, srcKind, _, err := credentialType(c, srcType)
			if err != nil {
				return err
			}
			if srcKind != "external" {
				return api.FieldError("source_credential", "Source must be an external credential.")
			}
			tgtSchema, tgtKind, _, err := credentialType(c, tgtType)
			if err != nil {
				return err
			}
			if tgtKind == "external" {
				return api.FieldError("target_credential", "Target must be a non-external credential.")
			}
			if tgtSchema.Field(str(merged["input_field_name"])) == nil {
				return api.FieldError("input_field_name", fmt.Sprintf("Input field %q not defined for the target credential type.", str(merged["input_field_name"])))
			}
			md, _ := normalize(merged["metadata"]).(map[string]any)
			if md == nil {
				md = map[string]any{}
			}
			if errs := credtypes.ValidateMetadata(srcSchema, md); len(errs) > 0 {
				return &api.Error{Status: 400, Body: Obj{"metadata": errs}}
			}
			return nil
		},
	})
}

func checkInputSource(c *Ctx, data Obj) error {
	if err := api.RequirePermID(c, "target_credential", "credentials", data["target_credential"], "change"); err != nil {
		return err
	}
	return api.RequirePermID(c, "source_credential", "credentials", data["source_credential"], "use")
}

// viewOf returns the view predicate of a resource (alias t) for use in subqueries.
func viewOf(c *Ctx, res *Resource, arg ArgFunc) string {
	if res.View != nil {
		return res.View(c, arg)
	}
	return c.User.Predicate(rbacArg(arg), res.ContentType, "view_"+res.ContentType, res.Scope("t"))
}

func testLookup(c *Ctx, w http.ResponseWriter, namespace string, inputs, metadata map[string]any) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, err := credplugins.Lookup(c, namespace, inputs, metadata); err != nil {
		msg := err.Error()
		msg = strings.TrimPrefix(msg, "vault: ")
		api.WriteJSON(w, 400, Obj{"inputs": msg})
		return
	}
	api.WriteJSON(w, 202, Obj{})
}
