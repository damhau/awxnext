package resources

import (
	"context"
	"net/http"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// Install registers non-resource routes (ping, config, settings, ...).
func Install(app *api.App) {
	installSettings(app)
	installBulk(app)
	installDashboard(app)
	app.HandlePublic("GET", "/api/v2/ping/", ping)
	app.Handle("GET", "/api/v2/config/", configView)
	installScheduleRoutes(app)
	installWebhookRoutes(app)
	installCallbackRoutes(app)
	app.Handle("GET", "/api/v2/metrics/", handleAWXMetrics)
	app.Handle("GET", "/api/v2/applications/", applications)
	app.Handle("OPTIONS", "/api/v2/applications/", applications)
}

// applications stands in for AWX's OAuth2 applications, which are not
// supported (personal tokens replace them): always empty and marked
// deprecated, so awxkit's export and import skip it.
func applications(c *Ctx, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Warning", `299 awxnext "deprecated: OAuth2 applications are not supported, use personal tokens"`)
	w.Header().Set("Allow", "GET, HEAD, OPTIONS")
	if r.Method == "OPTIONS" {
		api.WriteJSON(w, 200, Obj{"name": "Applications", "description": "OAuth2 applications are not supported by awxnext: use personal tokens.",
			"renders": []string{"application/json"}, "parses": []string{"application/json"}})
		return
	}
	api.WriteJSON(w, 200, Obj{"count": 0, "next": nil, "previous": nil, "results": []any{}})
}

// Bootstrap seeds data required at startup: managed roles, install uuid, this instance.
func Bootstrap(ctx context.Context, app *api.App) error {
	if err := rbac.SyncManaged(ctx, app.DB); err != nil {
		return err
	}
	if err := SeedCredentialTypes(ctx, app); err != nil {
		return err
	}
	// AWX's public Galaxy credential, which organizations pick by name. It
	// is read-only, so this only creates it on the first start.
	if _, err := app.DB.Exec(ctx, `INSERT INTO credentials (name, credential_type_id, inputs, managed)
		SELECT 'Ansible Galaxy', id, '{"url": "https://galaxy.ansible.com/"}', true FROM credential_types
		WHERE namespace = 'galaxy_api_token' AND managed AND NOT EXISTS (SELECT 1 FROM credentials WHERE managed)`); err != nil {
		return err
	}
	if !app.Settings.IsSet(ctx, "INSTALL_UUID") {
		var uuid string
		if err := app.DB.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&uuid); err != nil {
			return err
		}
		if err := app.Settings.Set(ctx, "INSTALL_UUID", uuid); err != nil {
			return err
		}
	}
	_, err := app.DB.Exec(ctx, `INSERT INTO instances (hostname, node_type, node_state, capacity, managed, version, last_seen, internal_url)
		VALUES ($1, 'hybrid', 'ready', $2, true, $3, now(), $4)
		ON CONFLICT (hostname) DO UPDATE SET last_seen = now(), version = EXCLUDED.version, capacity = EXCLUDED.capacity,
			internal_url = EXCLUDED.internal_url`,
		app.Cfg.NodeName, app.Cfg.Capacity, api.ProductVersion, app.Cfg.InternalURL)
	if err != nil {
		return err
	}
	if err := SeedInfrastructure(ctx, app); err != nil {
		return err
	}
	if err := SeedSystemJobTemplates(ctx, app); err != nil {
		return err
	}
	// Legacy object roles for objects created outside the API (migrations, imports).
	for _, res := range api.Resources() {
		if res.ContentType == "" || len(rbac.LegacyRoles(res.ContentType)) == 0 {
			continue
		}
		where := ""
		if res.Discriminator {
			where = " WHERE type = '" + res.Type + "'"
		}
		for _, m := range rbac.LegacyRoles(res.ContentType) {
			if _, err := app.DB.Exec(ctx, `INSERT INTO object_roles (content_type, object_id, role_field, role_definition_id)
				SELECT $1, t.id, $2, (SELECT id FROM role_definitions WHERE name = $3) FROM `+res.Table+` t`+where+`
				ON CONFLICT DO NOTHING`, res.ContentType, m.LegacyField, m.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func ping(c *Ctx, w http.ResponseWriter, r *http.Request) {
	type inst struct {
		Node      string `json:"node"`
		NodeType  string `json:"node_type"`
		UUID      string `json:"uuid"`
		Heartbeat string `json:"heartbeat"`
		Capacity  int    `json:"capacity"`
		Version   string `json:"version"`
	}
	instances := []inst{}
	rows, err := c.App.DB.Query(c, "SELECT hostname, node_type, uuid, coalesce(last_seen, created), capacity, version FROM instances ORDER BY hostname")
	if err == nil {
		for rows.Next() {
			var i inst
			var hb time.Time
			if rows.Scan(&i.Node, &i.NodeType, &i.UUID, &hb, &i.Capacity, &i.Version) == nil {
				i.Heartbeat = api.FormatTime(hb)
				instances = append(instances, i)
			}
		}
		rows.Close()
	}
	groups := []Obj{}
	rows, err = c.App.DB.Query(c, `SELECT g.name, coalesce(sum(i.capacity), 0)::int, coalesce(array_agg(i.hostname) FILTER (WHERE i.id IS NOT NULL), '{}')
		FROM instance_groups g LEFT JOIN instance_group_instances gi ON gi.instance_group_id = g.id LEFT JOIN instances i ON i.id = gi.instance_id
		GROUP BY g.name ORDER BY g.name`)
	if err == nil {
		for rows.Next() {
			var name string
			var capacity int
			var hosts []string
			if rows.Scan(&name, &capacity, &hosts) == nil {
				groups = append(groups, Obj{"name": name, "capacity": capacity, "instances": hosts})
			}
		}
		rows.Close()
	}
	api.WriteJSON(w, 200, Obj{
		"ha":              len(instances) > 1,
		"version":         api.ProductVersion,
		"active_node":     c.App.Cfg.NodeName,
		"install_uuid":    c.App.Settings.String(c, "INSTALL_UUID"),
		"instances":       instances,
		"instance_groups": groups,
	})
}

func configView(c *Ctx, w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, 200, Obj{
		"time_zone":            "UTC",
		"version":              api.ProductVersion,
		"eula":                 "",
		"analytics_status":     "off",
		"analytics_collectors": Obj{},
		"license_info":         Obj{"license_type": "open", "valid_key": true, "subscription_name": "OPEN", "product_name": "AWX"},
		"become_methods": [][]string{
			{"sudo", "Sudo"}, {"su", "Su"}, {"pbrun", "Pbrun"}, {"pfexec", "Pfexec"}, {"dzdo", "DZDO"},
			{"pmrun", "Pmrun"}, {"runas", "Runas"}, {"enable", "Enable"}, {"doas", "Doas"}, {"ksu", "Ksu"},
			{"machinectl", "Machinectl"}, {"sesu", "Sesu"},
		},
		"ui_next":             true,
		"project_base_dir":    c.App.Cfg.ProjectsRoot,
		"project_local_paths": []string{},
		"custom_virtualenvs":  []string{},
	})
}
