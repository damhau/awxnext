package resources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/damhau/awxnext/internal/api"
)

// EDA's credential types, credentials and input sources are awxnext's.

func edaCredentialTypes() *edaSpec {
	names := map[string]string{"name": "name", "description": "description", "kind": "kind", "inputs": "inputs", "injectors": "injectors"}
	return &edaSpec{
		path: "credential-types", res: "credential_types",
		filters: map[string]string{"name": "name__istartswith", "namespace": "namespace__istartswith", "kind": "kind"},
		names:   edaInvert(names),
		out: func(c *Ctx, o Obj) (Obj, error) {
			return edaCommon(o, Obj{"name": o["name"], "namespace": o["namespace"], "kind": o["kind"], "description": o["description"],
				"inputs": o["inputs"], "injectors": o["injectors"], "managed": o["managed"]}), nil
		},
		in: func(c *Ctx, body, old Obj) (Obj, error) {
			data := edaCopy(body, names)
			if old == nil && str(data["kind"]) == "" {
				data["kind"] = "cloud"
			}
			return data, nil
		},
	}
}

func edaTypeRef(c *Ctx, id any) (Obj, error) {
	tid, ok := api.ID(id)
	if !ok {
		return nil, nil
	}
	var name, ns, kind string
	if err := c.DB().QueryRow(c, "SELECT name, coalesce(namespace, ''), kind FROM credential_types WHERE id = $1", tid).Scan(&name, &ns, &kind); err != nil {
		return nil, err
	}
	return Obj{"id": tid, "name": name, "namespace": ns, "kind": kind}, nil
}

var edaCredentialNames = map[string]string{"name": "name", "description": "description", "inputs": "inputs",
	"credential_type_id": "credential_type", "organization_id": "organization"}

func edaCredentials() *edaSpec {
	return &edaSpec{
		path: "eda-credentials", res: "credentials",
		filters: map[string]string{"name": "name__istartswith", "credential_type_id": "credential_type",
			"credential_type__namespace": "credential_type__namespace", "credential_type__namespace__in": "credential_type__namespace__in",
			"credential_type__namespace__not_in": "not__credential_type__namespace__in",
			"credential_type__kind":              "credential_type__kind", "credential_type__kind__in": "credential_type__kind__in"},
		names: edaInvert(edaCredentialNames),
		out:   edaCredentialOut,
		in: func(c *Ctx, body, old Obj) (Obj, error) {
			data := edaCopy(body, edaCredentialNames)
			name, org := data["name"], data["organization"]
			if old != nil {
				if _, ok := data["name"]; !ok {
					name = old["name"]
				}
				if _, ok := data["organization"]; !ok {
					org = old["organization"]
				}
			}
			return data, edaUniqueCredential(c, str(name), org, old)
		},
	}
}

func edaCredentialOut(c *Ctx, o Obj) (Obj, error) {
	typ, err := edaTypeRef(c, o["credential_type"])
	if err != nil {
		return nil, err
	}
	org, err := edaOrgRef(c, o["organization"])
	if err != nil {
		return nil, err
	}
	return edaCommon(o, Obj{"name": o["name"], "description": o["description"], "managed": o["managed"], "inputs": o["inputs"],
		"credential_type": typ, "credential_type_id": o["credential_type"], "organization": org, "organization_id": o["organization"],
		"references": []any{}}), nil
}

// edaUniqueCredential checks EDA's rule: one credential of a name in an
// organization, whatever its type (awxnext's is per type).
func edaUniqueCredential(c *Ctx, name string, org any, old Obj) error {
	var self int64
	if old != nil {
		self, _ = api.ID(old["id"])
	}
	oid, _ := api.ID(org)
	var taken bool
	err := c.DB().QueryRow(c, `SELECT EXISTS (SELECT 1 FROM credentials WHERE name = $1 AND coalesce(organization_id, 0) = $2 AND id <> $3)`,
		name, oid, self).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return api.FieldError("name", "eda credential with this name already exists.")
	}
	return nil
}

var edaInputSourceNames = map[string]string{"source_credential": "source_credential", "target_credential": "target_credential",
	"input_field_name": "input_field_name", "description": "description", "metadata": "metadata"}

func edaInputSources() *edaSpec {
	return &edaSpec{
		path: "credential-input-sources", res: "credential_input_sources",
		filters: map[string]string{"source_credential": "source_credential", "target_credential": "target_credential"},
		names:   edaInvert(edaInputSourceNames),
		out:     edaInputSourceOut,
		in: func(c *Ctx, body, old Obj) (Obj, error) {
			data := edaCopy(body, edaInputSourceNames)
			if v, ok := data["description"]; ok && v == nil {
				data["description"] = ""
			}
			if v, ok := data["metadata"]; ok && v == nil {
				data["metadata"] = Obj{}
			}
			return data, nil
		},
	}
}

func edaInputSourceOut(c *Ctx, o Obj) (Obj, error) {
	var org any
	if tid, ok := api.ID(o["target_credential"]); ok {
		var oid *int64
		if err := c.DB().QueryRow(c, "SELECT organization_id FROM credentials WHERE id = $1", tid).Scan(&oid); err != nil {
			return nil, err
		}
		if oid != nil {
			ref, err := edaOrgRef(c, *oid)
			if err != nil {
				return nil, err
			}
			org = ref
		}
	}
	return edaCommon(o, Obj{"source_credential": o["source_credential"], "target_credential": o["target_credential"],
		"input_field_name": o["input_field_name"], "description": o["description"], "metadata": o["metadata"],
		"organization": org, "references": []any{}}), nil
}

func installEDACredentialActions(app *api.App) {
	app.Handle("POST", edaRoot+"credential-types/{id}/test/", edaCredentialTypeTest)
	app.Handle("POST", edaRoot+"eda-credentials/{id}/test/", edaCredentialTest)
	app.Handle("POST", edaRoot+"eda-credentials/{id}/copy/", edaCredentialCopy)
	app.Handle("GET", edaRoot+"eda-credentials/{id}/input_sources/", edaCredentialInputSources)
}

// edaAction runs an awxnext action of obj with body, as its own view does.
func edaAction(c *Ctx, w http.ResponseWriter, r *http.Request, res *api.Resource, name string, obj Obj, body Obj) {
	for _, a := range res.Actions {
		if a.Name != name {
			continue
		}
		if a.Perm != "" {
			ok, err := api.HasPerm(c, res, obj, a.Perm)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			if !ok {
				api.WriteError(w, api.ErrForbidden)
				return
			}
		}
		b, err := json.Marshal(body)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(b))
		r2.ContentLength = int64(len(b))
		a.Handler(c, w, r2, obj)
		return
	}
	api.WriteError(w, api.Detail(404, "Not found."))
}

func edaCredentialTypeTest(c *Ctx, w http.ResponseWriter, r *http.Request) {
	s := edaCredentialTypes()
	obj, err := s.object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if _, ok := body["inputs"].(map[string]any); !ok {
		api.WriteError(w, api.FieldError("inputs", "This field is required."))
		return
	}
	edaAction(c, w, r, api.Lookup("credential_types"), "test", obj, Obj{"inputs": body["inputs"], "metadata": body["metadata"]})
}

func edaCredentialTest(c *Ctx, w http.ResponseWriter, r *http.Request) {
	obj, err := edaCredentials().object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	edaAction(c, w, r, api.Lookup("credentials"), "test", obj, Obj{"metadata": body["metadata"]})
}

func edaCredentialCopy(c *Ctx, w http.ResponseWriter, r *http.Request) {
	s := edaCredentials()
	obj, err := s.object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	name := strings.TrimSpace(str(body["name"]))
	if name == "" {
		api.WriteError(w, api.FieldError("name", "This field is required."))
		return
	}
	var out Obj
	err = c.WithTx(func(c *Ctx) error {
		if err := edaUniqueCredential(c, name, obj["organization"], nil); err != nil {
			return err
		}
		created, err := api.Copy(c, api.Lookup("credentials"), obj, name)
		if err != nil {
			return err
		}
		out, err = s.renderOne(c, created)
		return err
	})
	if err != nil {
		writeEDAError(w, err, s.names)
		return
	}
	api.WriteJSON(w, 201, out)
}

func edaCredentialInputSources(c *Ctx, w http.ResponseWriter, r *http.Request) {
	obj, err := edaCredentials().object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	page, size, err := edaPage(r.URL.Query())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	s := edaInputSources()
	res := api.Lookup(s.res)
	objs, count, err := api.List(c, res, api.ListParams{Params: url.Values{"target_credential": {fmt.Sprint(obj["id"])}},
		Page: page, PageSize: size, DefaultOrder: "t.id"})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	results, err := s.render(c, objs)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	writeEDAPage(w, r, count, page, size, results)
}
