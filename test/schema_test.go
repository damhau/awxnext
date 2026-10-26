package test

import (
	"testing"

	"github.com/damhau/awxnext/internal/api"
)

// TestResourcesMatchSchema lists every resource and checks that the audit
// columns the engine writes exist, so a resource/table mismatch fails here
// rather than on the first create.
func TestResourcesMatchSchema(t *testing.T) {
	a := adminClient(t)
	for _, res := range api.Resources() {
		r := a.get(res.Name + "/?page_size=1")
		if r.Status != 200 {
			t.Errorf("%s list: %d %.200s", res.Name, r.Status, r.Raw)
		}
		if r := a.do("OPTIONS", res.Name+"/", nil); r.Status != 200 {
			t.Errorf("%s OPTIONS: %d", res.Name, r.Status)
		}
		if res.NoTimestamps {
			continue
		}
		for _, col := range []string{"created", "modified", "created_by_id", "modified_by_id"} {
			var ok bool
			app.DB.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`, res.Table, col).Scan(&ok)
			if !ok {
				t.Errorf("%s: table %s has no column %s", res.Name, res.Table, col)
			}
		}
	}
}
