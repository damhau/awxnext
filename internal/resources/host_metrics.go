package resources

import (
	"github.com/damhau/awxnext/internal/api"
)

// adminOrAuditor shows rows to superusers and system auditors only, as AWX's
// IsSystemAdminOrAuditor.
func adminOrAuditor(c *Ctx, arg ArgFunc) string {
	if c.User.Superuser || c.User.Auditor {
		return "TRUE"
	}
	return "FALSE"
}

// registerHostMetrics adds AWX's /host_metrics/ (each host name automated,
// collected per job) and /host_metric_summary_monthly/.
func registerHostMetrics() {
	api.Register(&Resource{
		Name:         "host_metrics",
		Type:         "host_metric",
		Table:        "host_metrics",
		Verbose:      "Host Metric",
		Ordering:     "hostname",
		NoTimestamps: true,
		NoName:       true,
		NoActivity:   true,
		NoCreate:     true,
		NoUpdate:     true,
		Fields: []*Field{
			{Name: "hostname", Type: api.TString, ReadOnly: true},
			{Name: "first_automation", Type: api.TDateTime, ReadOnly: true, Help: "When the host was first automated against"},
			{Name: "last_automation", Type: api.TDateTime, ReadOnly: true, Help: "When the host was last automated against"},
			{Name: "last_deleted", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "When the host was last deleted"},
			{Name: "automated_counter", Type: api.TInt, ReadOnly: true, Help: "How many times was the host automated"},
			{Name: "deleted_counter", Type: api.TInt, ReadOnly: true, Help: "How many times was the host deleted"},
			{Name: "deleted", Type: api.TBool, ReadOnly: true, Help: "Boolean flag saying whether the host is deleted and therefore not counted into the subscription consumption"},
			{Name: "used_in_inventories", Type: api.TInt, ReadOnly: true, Nullable: true, Help: "How many inventories contain this host"},
		},
		SearchFields: []string{"hostname"},
		View:         adminOrAuditor,
		CanDelete: func(c *Ctx, obj Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			return nil
		},
		// As AWX: a deleted host metric stays, marked deleted, and counts
		// again when the host is automated again.
		SoftDelete: func(c *Ctx, obj Obj) error {
			_, err := c.DB().Exec(c, `UPDATE host_metrics SET deleted = true, deleted_counter = deleted_counter + 1, last_deleted = now()
				WHERE id = $1 AND NOT deleted`, obj["id"])
			return err
		},
		Capabilities: func(c *Ctx, obj Obj, perms map[string]bool) map[string]bool {
			return map[string]bool{"delete": c.User.Superuser}
		},
	})
	api.Register(&Resource{
		Name:         "host_metric_summary_monthly",
		Type:         "host_metric_summary_monthly",
		Table:        "host_metric_summary_monthly",
		Verbose:      "Host Metric Summary Monthly",
		Ordering:     "date",
		NoTimestamps: true,
		NoName:       true,
		NoActivity:   true,
		ReadOnly:     true,
		Fields: []*Field{
			{Name: "date", Type: api.TString, ReadOnly: true},
			{Name: "license_consumed", Type: api.TInt, ReadOnly: true, Help: "How many unique hosts are consumed from the license"},
			{Name: "license_capacity", Type: api.TInt, ReadOnly: true, Help: "'License capacity as max. number of unique hosts"},
			{Name: "hosts_added", Type: api.TInt, ReadOnly: true, Help: "How many hosts were added in the associated month, consuming more license capacity"},
			{Name: "hosts_deleted", Type: api.TInt, ReadOnly: true, Help: "How many hosts were deleted in the associated month, freeing the license capacity"},
			{Name: "indirectly_managed_hosts", Type: api.TInt, ReadOnly: true, Help: "Manually entered number indirectly managed hosts for a certain month"},
		},
		SearchFields: []string{"date"},
		View:         adminOrAuditor,
		// A date, as AWX writes it: 2026-10-01.
		Serialize: func(c *Ctx, obj Obj) {
			if s, ok := obj["date"].(string); ok && len(s) > 10 {
				obj["date"] = s[:10]
			}
		},
	})
}
