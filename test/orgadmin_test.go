package test

import (
	"fmt"
	"testing"
)

func TestOrgAdminSettings(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() {
		a.patch("settings/system/", map[string]any{"ORG_ADMINS_CAN_SEE_ALL_USERS": true, "MANAGE_ORGANIZATION_AUTH": true})
	})
	mine, other := newOrg(t, a), newOrg(t, a)
	boss, bossID := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/admins/", mine), map[string]any{"id": bossID}), 204)
	_, outsiderID := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", other), map[string]any{"id": outsiderID}), 204)
	otherTeam := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": other}), 201).id()
	_, newcomerID := newUser(t, a)

	// ORG_ADMINS_CAN_SEE_ALL_USERS: on (AWX's default), an organization admin
	// sees users and teams of other organizations; off, only their own.
	must(t, boss.get(fmt.Sprintf("users/%d/", outsiderID)), 200)
	must(t, boss.get(fmt.Sprintf("teams/%d/", otherTeam)), 200)
	must(t, a.patch("settings/system/", map[string]any{"ORG_ADMINS_CAN_SEE_ALL_USERS": false}), 200)
	must(t, boss.get(fmt.Sprintf("users/%d/", outsiderID)), 403)
	must(t, boss.get(fmt.Sprintf("teams/%d/", otherTeam)), 403)
	if must(t, boss.get(fmt.Sprintf("users/?id=%d", outsiderID)), 200).count() != 0 {
		t.Fatal("the user list still shows users of other organizations")
	}
	must(t, boss.get(fmt.Sprintf("users/%d/", bossID)), 200)

	// MANAGE_ORGANIZATION_AUTH: on, an organization admin manages users,
	// teams and membership of their organization; off, only superusers do.
	// As in AWX, they create users through their organization, where the user
	// joins it, and not at /users/, where the user would belong to none.
	must(t, boss.post("users/", map[string]any{"username": uniq("made"), "password": "pw"}), 403)
	made := must(t, boss.post(fmt.Sprintf("organizations/%d/users/", mine), map[string]any{"username": uniq("made"), "password": "pw"}), 201).id()
	if must(t, a.get(fmt.Sprintf("organizations/%d/users/?id=%d", mine, made)), 200).count() != 1 {
		t.Error("a user created through the organization is not a member of it")
	}
	stray := uniq("made")
	must(t, boss.post(fmt.Sprintf("organizations/%d/users/", other), map[string]any{"username": stray, "password": "pw"}), 403)
	if must(t, a.get("users/?username="+stray), 200).count() != 0 {
		t.Error("a refused create through another organization left the user behind")
	}
	must(t, boss.post(fmt.Sprintf("organizations/%d/users/", mine), map[string]any{"username": uniq("made"), "password": "pw", "is_superuser": true}), 403)
	team := must(t, boss.post("teams/", map[string]any{"name": uniq("team"), "organization": mine}), 201).id()
	must(t, boss.post(fmt.Sprintf("organizations/%d/users/", mine), map[string]any{"id": newcomerID}), 204)
	must(t, boss.post(fmt.Sprintf("teams/%d/users/", team), map[string]any{"id": newcomerID}), 204)

	must(t, a.patch("settings/system/", map[string]any{"MANAGE_ORGANIZATION_AUTH": false}), 200)
	must(t, boss.post(fmt.Sprintf("organizations/%d/users/", mine), map[string]any{"username": uniq("made"), "password": "pw"}), 403)
	must(t, boss.post("teams/", map[string]any{"name": uniq("team"), "organization": mine}), 403)
	must(t, boss.patch(fmt.Sprintf("teams/%d/", team), map[string]any{"description": "x"}), 403)
	must(t, boss.post(fmt.Sprintf("organizations/%d/users/", mine), map[string]any{"id": newcomerID, "disassociate": true}), 403)
	must(t, boss.post(fmt.Sprintf("teams/%d/users/", team), map[string]any{"id": newcomerID, "disassociate": true}), 403)
	memberRole := must(t, a.get(fmt.Sprintf("teams/%d/", team)), 200).Body["summary_fields"].(map[string]any)["object_roles"].(map[string]any)["member_role"].(map[string]any)["id"]
	must(t, boss.post(fmt.Sprintf("users/%d/roles/", newcomerID), map[string]any{"id": memberRole, "disassociate": true}), 403)
	// Their own profile is still theirs, and superusers still manage everything.
	must(t, boss.patch(fmt.Sprintf("users/%d/", bossID), map[string]any{"first_name": "Boss"}), 200)
	must(t, a.post(fmt.Sprintf("teams/%d/users/", team), map[string]any{"id": newcomerID, "disassociate": true}), 204)
}
