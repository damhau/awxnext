package test

import (
	"fmt"
	"testing"
)

// ACTIVITY_STREAM_ENABLED off records nothing, except turning it off; version
// history is a separate feature and keeps recording.
func TestActivityStreamSwitch(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() {
		a.patch("settings/system/", map[string]any{"ACTIVITY_STREAM_ENABLED": true, "SUBSCRIPTIONS_USERNAME": ""})
	})
	org := newOrg(t, a)
	settingEntries := func() int { return must(t, a.get("activity_stream/?object1=setting"), 200).count() }

	on := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": org}), 201).id()
	if n := must(t, a.get(fmt.Sprintf("teams/%d/activity_stream/", on)), 200).count(); n != 1 {
		t.Fatalf("stream on: %d entries for a new team, want 1", n)
	}

	before := settingEntries()
	must(t, a.patch("settings/system/", map[string]any{"ACTIVITY_STREAM_ENABLED": false}), 200)
	if settingEntries() != before+1 {
		t.Fatal("turning the activity stream off was not recorded")
	}
	off := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": org}), 201).id()
	must(t, a.patch(fmt.Sprintf("teams/%d/", off), map[string]any{"description": "changed"}), 200)
	must(t, a.post(fmt.Sprintf("teams/%d/users/", off), map[string]any{"id": newUserID(t, a)}), 204)
	if n := must(t, a.get(fmt.Sprintf("teams/%d/activity_stream/", off)), 200).count(); n != 0 {
		t.Fatalf("stream off: %d entries", n)
	}
	must(t, a.patch("settings/system/", map[string]any{"SUBSCRIPTIONS_USERNAME": "someone"}), 200)
	if settingEntries() != before+1 {
		t.Fatal("a settings change was recorded while the stream is off")
	}
	if n := must(t, a.get(fmt.Sprintf("teams/%d/history/", off)), 200).count(); n == 0 {
		t.Fatal("version history stopped with the activity stream")
	}
}

func newUserID(t *testing.T, a *client) int64 {
	t.Helper()
	_, id := newUser(t, a)
	return id
}

// ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC (off by default) records the
// hosts and groups a sync adds and removes.
func TestActivityStreamForInventorySync(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	t.Cleanup(func() {
		a.patch("settings/system/", map[string]any{"ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC": false})
	})
	org := newOrg(t, a)
	repo := gitRepo(t, map[string]string{"inventories/lab.yml": scmInventory})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo,
		"scm_update_on_launch": true}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "lab", "inventory": inv, "source": "scm", "source_project": proj,
		"source_path": "inventories/lab.yml", "overwrite": true}), 201).id()
	entries := func(op, typ, name string) int {
		var n int
		if err := app.DB.QueryRow(t.Context(), `SELECT count(*) FROM activity_stream WHERE object2 = 'inventory' AND object2_id = $1
			AND operation = $2 AND object1 = $3 AND changes->>'name' = $4`, inv, op, typ, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if entries("create", "host", "db1") != 0 {
		t.Fatal("the sync was recorded while the switch is off")
	}

	must(t, a.patch("settings/system/", map[string]any{"ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC": true}), 200)
	commitFiles(t, repo, map[string]string{"inventories/lab.yml": scmInventoryV2}, "shrink")
	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if entries("create", "host", "web3") != 1 || entries("delete", "host", "db1") != 1 || entries("delete", "group", "db") != 1 {
		t.Fatalf("sync entries: create web3 %d, delete db1 %d, delete group db %d",
			entries("create", "host", "web3"), entries("delete", "host", "db1"), entries("delete", "group", "db"))
	}
}
