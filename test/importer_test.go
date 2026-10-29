package test

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/importer"
	"github.com/jackc/pgx/v5"
)

// Values encrypted by AWX's encrypt_field with SECRET_KEY "awx-secret-key":
// credential 42 field "password", survey default (no pk, field "value"),
// notification template 7 field "notification_configuration".
const (
	awxCredSecret   = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnEzN2RVVUZyN0FpRVJqX3ZoODJUaVdGYTlWaTZIcklNMF9keDVWWFlQMk1mVEl3QU1WUFpTR2dVRUNZWXE0amx5RUMyMzQwa1dKY04tZVNpeDRvdEdPVzJ5TkE9PQ=="
	awxSurveySecret = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnEzN2RWREdoTnlEZFZRWU5rOUtsOVIzR3ZneV9wQmpmbU5QdnRLUUIwWWVjdG1KWlZfM0N6d3dRNFU3ODVqbGZlRVp4aW5ENl9XUnltcUdLbHUyRFEzVmFHNEE9PQ=="
	// AUTH_LDAP_BIND_PASSWORD "svc-pw", as AWX stores conf_setting 7.
	awxBindPassword = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnE0MS1YcTRRbnJyX1dxZXZqZVZUZWppbnBSX2NZY1R3SkZOQVVCcTVoOVVyZGRTWno0cGdXaFVVdjJJXzRlVkV4TDNaMzhwTWp5MU9GSXVaU3c3TWg3a0kxNUE9PQ=="
	awxNotifySecret = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnEzN2RWcVBOQ2lxODhwMHRjaTdSTDNyUDNxWmxDbU1kNHgzWGtLMDlySkh3R2ZjWnhFYlF1VzVZVVJMc1FFM25tNTN2UzdwSHdyUUs1Y3RoeDdBX0lHUUZIcUE9PQ=="
)

const awxSchema = `
CREATE TABLE django_content_type (id int PRIMARY KEY, app_label text, model text);
CREATE TABLE conf_setting (id int PRIMARY KEY, created timestamptz, modified timestamptz, key text, value jsonb, user_id int);
CREATE TABLE main_profile (id int PRIMARY KEY, created timestamptz, modified timestamptz, ldap_dn text, user_id int);
CREATE TABLE social_auth_usersocialauth (id int PRIMARY KEY, provider text, uid text, extra_data jsonb, user_id int, created timestamptz, modified timestamptz);
CREATE TABLE auth_user (id int PRIMARY KEY, username text, first_name text, last_name text, email text, password text,
  is_superuser bool, is_active bool, last_login timestamptz, date_joined timestamptz);
CREATE TABLE main_rbac_roles (id int PRIMARY KEY, role_field text, singleton_name text, implicit_parents text, content_type_id int, object_id int);
CREATE TABLE main_rbac_roles_members (id serial PRIMARY KEY, role_id int, user_id int);
CREATE TABLE main_rbac_roles_parents (id serial PRIMARY KEY, from_role_id int, to_role_id int);
CREATE TABLE main_organization (id int PRIMARY KEY, name text, description text, created timestamptz, modified timestamptz,
  created_by_id int, modified_by_id int, max_hosts int, default_environment_id int);
CREATE TABLE main_team (id int PRIMARY KEY, name text, description text, organization_id int, created timestamptz, modified timestamptz,
  created_by_id int, modified_by_id int);
CREATE TABLE main_credentialtype (id int PRIMARY KEY, name text, description text, kind text, inputs jsonb, injectors jsonb,
  created timestamptz, modified timestamptz, created_by_id int, modified_by_id int, managed bool, namespace text);
CREATE TABLE main_credential (id int PRIMARY KEY, name text, description text, organization_id int, credential_type_id int, inputs jsonb,
  created timestamptz, modified timestamptz, created_by_id int, modified_by_id int, managed bool);
CREATE TABLE main_executionenvironment (id int PRIMARY KEY, name text, description text, image text, managed bool, organization_id int,
  credential_id int, pull text, created timestamptz, modified timestamptz, created_by_id int, modified_by_id int);
CREATE TABLE main_instancegroup (id int PRIMARY KEY, name text, created timestamptz, modified timestamptz, is_container_group bool,
  credential_id int, pod_spec_override text, policy_instance_percentage int, policy_instance_minimum int, max_concurrent_jobs int, max_forks int);
CREATE TABLE main_label (id int PRIMARY KEY, name text, organization_id int, created timestamptz, modified timestamptz, created_by_id int, modified_by_id int);
CREATE TABLE main_inventory (id int PRIMARY KEY, name text, description text, organization_id int, variables text, created timestamptz,
  modified timestamptz, created_by_id int, modified_by_id int, kind text, host_filter text, prevent_instance_group_fallback bool, pending_deletion bool);
CREATE TABLE main_host (id int PRIMARY KEY, name text, description text, inventory_id int, enabled bool, instance_id text, variables text,
  created timestamptz, modified timestamptz, created_by_id int, modified_by_id int, ansible_facts jsonb, ansible_facts_modified timestamptz);
CREATE TABLE main_group (id int PRIMARY KEY, name text, description text, inventory_id int, variables text, created timestamptz,
  modified timestamptz, created_by_id int, modified_by_id int);
CREATE TABLE main_group_hosts (id serial PRIMARY KEY, group_id int, host_id int);
CREATE TABLE main_group_parents (id serial PRIMARY KEY, from_group_id int, to_group_id int);
CREATE TABLE main_unifiedjobtemplate (id int PRIMARY KEY, name text, description text, created timestamptz, modified timestamptz,
  created_by_id int, modified_by_id int, organization_id int, execution_environment_id int, polymorphic_ctype_id int);
CREATE TABLE main_project (unifiedjobtemplate_ptr_id int PRIMARY KEY, local_path text, scm_type text, scm_url text, scm_branch text,
  scm_refspec text, scm_clean bool, scm_delete_on_update bool, scm_track_submodules bool, credential_id int, timeout int,
  scm_update_on_launch bool, scm_update_cache_timeout int, allow_override bool, default_environment_id int, signature_validation_credential_id int);
CREATE TABLE main_jobtemplate (unifiedjobtemplate_ptr_id int PRIMARY KEY, job_type text, playbook text, forks int, "limit" text, verbosity int,
  extra_vars text, job_tags text, force_handlers bool, skip_tags text, start_at_task text, become_enabled bool, host_config_key text,
  survey_enabled bool, survey_spec jsonb, inventory_id int, project_id int, timeout int, use_fact_cache bool, allow_simultaneous bool,
  diff_mode bool, job_slice_count int, scm_branch text, webhook_service text, webhook_credential_id int, webhook_key text,
  prevent_instance_group_fallback bool, ask_variables_on_launch bool, ask_limit_on_launch bool, ask_inventory_on_launch bool);
CREATE TABLE main_workflowjobtemplate (unifiedjobtemplate_ptr_id int PRIMARY KEY, extra_vars text, survey_enabled bool, survey_spec jsonb,
  allow_simultaneous bool, inventory_id int, "limit" text, scm_branch text, job_tags text, skip_tags text, webhook_service text,
  webhook_credential_id int, webhook_key text, ask_inventory_on_launch bool, ask_variables_on_launch bool);
CREATE TABLE main_workflowapprovaltemplate (unifiedjobtemplate_ptr_id int PRIMARY KEY, timeout int);
CREATE TABLE main_workflowjobtemplatenode (id int PRIMARY KEY, created timestamptz, modified timestamptz, workflow_job_template_id int,
  unified_job_template_id int, identifier text, all_parents_must_converge bool, extra_data jsonb, inventory_id int, execution_environment_id int,
  "limit" text, verbosity int);
CREATE TABLE main_workflowjobtemplatenode_success_nodes (id serial PRIMARY KEY, from_workflowjobtemplatenode_id int, to_workflowjobtemplatenode_id int);
CREATE TABLE main_inventorysource (unifiedjobtemplate_ptr_id int PRIMARY KEY, source text, source_path text, source_vars text, overwrite bool,
  overwrite_vars bool, timeout int, verbosity int, update_on_launch bool, update_cache_timeout int, inventory_id int, source_project_id int,
  enabled_var text, enabled_value text, host_filter text, "limit" text, scm_branch text);
CREATE TABLE main_systemjobtemplate (unifiedjobtemplate_ptr_id int PRIMARY KEY, job_type text);
CREATE TABLE main_unifiedjobtemplate_credentials (id serial PRIMARY KEY, unifiedjobtemplate_id int, credential_id int);
CREATE TABLE main_unifiedjobtemplate_labels (id serial PRIMARY KEY, unifiedjobtemplate_id int, label_id int);
CREATE TABLE main_schedule (id int PRIMARY KEY, name text, description text, enabled bool, rrule text, extra_data jsonb, unified_job_template_id int,
  created timestamptz, modified timestamptz, created_by_id int, modified_by_id int, inventory_id int, execution_environment_id int, "limit" text);
CREATE TABLE main_notificationtemplate (id int PRIMARY KEY, name text, description text, organization_id int, notification_type text,
  notification_configuration jsonb, messages jsonb, created timestamptz, modified timestamptz, created_by_id int, modified_by_id int);
CREATE TABLE main_unifiedjobtemplate_notification_templates_for_success (id serial PRIMARY KEY, unifiedjobtemplate_id int, notificationtemplate_id int);
CREATE TABLE main_organization_notification_templates_for_error (id serial PRIMARY KEY, organization_id int, notificationtemplate_id int);

INSERT INTO django_content_type VALUES (1,'main','organization'),(2,'main','team'),(3,'main','credential'),(4,'main','project'),
  (5,'main','inventory'),(6,'main','jobtemplate'),(7,'main','workflowjobtemplate'),(8,'main','workflowapprovaltemplate'),
  (9,'main','inventorysource'),(10,'main','systemjobtemplate');
`

func awxData(org, alicePW string) string {
	ts := "'2024-01-01T00:00:00Z'"
	return fmt.Sprintf(`
INSERT INTO auth_user VALUES (1,'admin','','','','x',true,true,NULL,%[1]s),(2,'alice-%[2]s','Alice','A','alice@example.com','%[3]s',false,true,NULL,%[1]s),
  (3,'bob-%[2]s','Bob','B','bob@example.com','!unusable',false,true,NULL,%[1]s),
  (4,'sam-%[2]s','Sam','S','sam@example.com','!unusable',false,true,NULL,%[1]s),
  (5,'gina-%[2]s','Gina','G','gina@example.com','!unusable',false,true,NULL,%[1]s);
INSERT INTO social_auth_usersocialauth VALUES (1,'saml','okta:sam@example.com','{}',4,%[1]s,%[1]s),(2,'github-org','583231','{}',5,%[1]s,%[1]s);
INSERT INTO main_profile VALUES (1,%[1]s,%[1]s,'',2),(2,%[1]s,%[1]s,'uid=bob,ou=people,dc=example,dc=com',3);
INSERT INTO conf_setting VALUES (7,%[1]s,%[1]s,'AUTH_LDAP_BIND_PASSWORD','"`+awxBindPassword+`"',NULL),
  (8,%[1]s,%[1]s,'AUTH_LDAP_SERVER_URI','"ldap://127.0.0.1:1"',NULL),
  (9,%[1]s,%[1]s,'AUTH_LDAP_USER_SEARCH','["ou=people,dc=example,dc=com", "SCOPE_SUBTREE", "(uid=%%(user)s)"]',NULL),
  (10,%[1]s,%[1]s,'AUTH_LDAP_ORGANIZATION_MAP','{"Default": {"users": true, "remove_users": false}}',NULL),
  (11,%[1]s,%[1]s,'SOCIAL_AUTH_TEAM_MAP','{"Web": {"organization": "Default", "users": "/^a/"}}',NULL),
  (12,%[1]s,%[1]s,'RADIUS_SERVER','"radius.example.com"',NULL),
  (13,%[1]s,%[1]s,'TOWER_URL_BASE','"https://awx.example.com"',NULL),
  (14,%[1]s,%[1]s,'AUTH_LDAP_GROUP_TYPE_PARAMS','{"colour": "blue"}',NULL),
  (15,%[1]s,%[1]s,'AUTH_LDAP_START_TLS','true',2);
INSERT INTO main_organization VALUES (1,'%[2]s','imported',%[1]s,%[1]s,1,1,5,NULL);
INSERT INTO main_team VALUES (1,'Ops','',1,%[1]s,%[1]s,1,1);
INSERT INTO main_credentialtype VALUES (1,'Machine','','ssh','{}','{}',%[1]s,%[1]s,NULL,NULL,true,'ssh'),
  (2,'AWS Secrets Manager Lookup','','external','{}','{}',%[1]s,%[1]s,NULL,NULL,true,'aws_secretsmanager_credential'),
  (3,'MyCloud-%[2]s','','cloud','{"fields":[{"id":"token","label":"Token","type":"string","secret":true}]}','{"env":{"MYCLOUD_TOKEN":"{{ token }}"}}',%[1]s,%[1]s,NULL,NULL,false,NULL);
INSERT INTO main_credential VALUES (42,'machine','',1,1,'{"username":"root","password":"%[4]s","become_method":"sudo"}',%[1]s,%[1]s,1,1,false),
  (43,'aws lookup','',1,2,'{}',%[1]s,%[1]s,1,1,false);
INSERT INTO main_executionenvironment VALUES (1,'AWX EE (latest)','','quay.io/ansible/awx-ee:latest',true,NULL,NULL,'',%[1]s,%[1]s,NULL,NULL);
INSERT INTO main_instancegroup VALUES (1,'default',%[1]s,%[1]s,false,NULL,'',100,0,0,0),(2,'remote-%[2]s',%[1]s,%[1]s,false,NULL,'',0,0,0,0);
INSERT INTO main_label VALUES (1,'prod',1,%[1]s,%[1]s,1,1);
INSERT INTO main_inventory VALUES (1,'inv','',1,'env: prod',%[1]s,%[1]s,1,1,'','',false,false),
  (2,'smart','',1,'',%[1]s,%[1]s,1,1,'smart','name__icontains=web',false,false),
  (3,'gone','',1,'',%[1]s,%[1]s,1,1,'','',false,true);
INSERT INTO main_host VALUES (1,'web1','',1,true,'','port: 22',%[1]s,%[1]s,1,1,'{"os":"linux"}',%[1]s),(2,'db1','',1,false,'','',%[1]s,%[1]s,1,1,'{}',NULL);
INSERT INTO main_group VALUES (1,'web','',1,'http: 80',%[1]s,%[1]s,1,1),(2,'web-a','',1,'',%[1]s,%[1]s,1,1);
INSERT INTO main_group_hosts (group_id, host_id) VALUES (1,1);
INSERT INTO main_group_parents (from_group_id, to_group_id) VALUES (2,1);
INSERT INTO main_unifiedjobtemplate VALUES (10,'git-proj','',%[1]s,%[1]s,1,1,1,NULL,4),(11,'svn-proj','',%[1]s,%[1]s,1,1,1,NULL,4),
  (20,'deploy','',%[1]s,%[1]s,1,1,1,NULL,6),(30,'pipeline','',%[1]s,%[1]s,1,1,1,NULL,7),(31,'Approve?','',%[1]s,%[1]s,1,1,1,NULL,8),
  (40,'from-git','',%[1]s,%[1]s,1,1,NULL,NULL,9),(50,'Cleanup Job Details','',%[1]s,%[1]s,NULL,NULL,NULL,NULL,10);
INSERT INTO main_project VALUES (10,'_10__git_proj','git','https://example.com/repo.git','main','',false,false,false,42,0,true,60,true,NULL,NULL),
  (11,'_11__svn','svn','svn://example.com/r','','',false,false,false,NULL,0,false,0,false,NULL,NULL);
INSERT INTO main_jobtemplate VALUES (20,'run','site.yml',5,'web*',1,'x: 1','','f','','',true,'cbkey',true,
  '{"name":"s","description":"","spec":[{"variable":"pw","question_name":"PW","type":"password","required":false,"default":"%[5]s"}]}',
  1,10,0,false,false,false,1,'','github',NULL,'hookkey',false,true,false,false);
INSERT INTO main_workflowjobtemplate VALUES (30,'a: 1',false,'{}',false,NULL,'','','','','',NULL,'',false,false);
INSERT INTO main_workflowapprovaltemplate VALUES (31,3600);
INSERT INTO main_workflowjobtemplatenode VALUES (1,%[1]s,%[1]s,30,20,'first',false,'{}',NULL,NULL,NULL,NULL),(2,%[1]s,%[1]s,30,31,'gate',false,'{}',NULL,NULL,NULL,NULL);
INSERT INTO main_workflowjobtemplatenode_success_nodes (from_workflowjobtemplatenode_id, to_workflowjobtemplatenode_id) VALUES (1,2);
INSERT INTO main_inventorysource VALUES (40,'scm','hosts.yml','',true,true,0,1,true,0,1,10,'','','','','');
INSERT INTO main_systemjobtemplate VALUES (50,'cleanup_jobs');
INSERT INTO main_unifiedjobtemplate_credentials (unifiedjobtemplate_id, credential_id) VALUES (20,42);
INSERT INTO main_unifiedjobtemplate_labels (unifiedjobtemplate_id, label_id) VALUES (20,1);
INSERT INTO main_schedule VALUES (1,'nightly','',true,'DTSTART;TZID=UTC:20240101T020000 RRULE:FREQ=DAILY','{}',20,%[1]s,%[1]s,1,1,NULL,NULL,'web1'),
  (2,'Cleanup Job Schedule','',true,'DTSTART:20240101T000000Z RRULE:FREQ=WEEKLY;BYDAY=SA','{"days":"30"}',50,%[1]s,%[1]s,NULL,NULL,NULL,NULL,NULL),
  (3,'broken','',true,'garbage','{}',20,%[1]s,%[1]s,1,1,NULL,NULL,NULL);
INSERT INTO main_notificationtemplate VALUES (7,'hook','',1,'webhook','{"url":"https://example.com/hook","username":"u","password":"%[6]s","http_method":"POST","headers":{},"disable_ssl_verification":false}',NULL,%[1]s,%[1]s,1,1),
  (8,'slack','',1,'slack','{"token":"x","channels":["#a"]}',NULL,%[1]s,%[1]s,1,1),
  (9,'mail','',1,'email','{"host":"smtp.example.com","port":587,"username":"","password":"","use_tls":true,"use_ssl":false,"sender":"awx@example.com","recipients":["ops@example.com"],"timeout":30}',NULL,%[1]s,%[1]s,1,1);
INSERT INTO main_unifiedjobtemplate_notification_templates_for_success (unifiedjobtemplate_id, notificationtemplate_id) VALUES (20,7);
INSERT INTO main_organization_notification_templates_for_error (organization_id, notificationtemplate_id) VALUES (1,7);
INSERT INTO main_rbac_roles VALUES (100,'admin_role',NULL,'[]',1,1),(101,'member_role',NULL,'[]',1,1),(102,'member_role',NULL,'[101]',2,1),
  (103,'execute_role',NULL,'[100]',6,20),(104,'use_role',NULL,'[100]',3,42),(105,NULL,'system_auditor','[]',NULL,NULL);
INSERT INTO main_rbac_roles_members (role_id, user_id) VALUES (100,2),(102,3),(105,3);
INSERT INTO main_rbac_roles_parents (from_role_id, to_role_id) VALUES (103,100),(103,102),(104,100),(104,102),(102,101);
`, ts, org, alicePW, awxCredSecret, awxSurveySecret, awxNotifySecret)
}

func djangoHash(pw string) string {
	salt := "abcdefgh12345678"
	key, _ := pbkdf2.Key(sha256.New, pw, []byte(salt), 1000, 32)
	return fmt.Sprintf("pbkdf2_sha256$1000$%s$%s", salt, base64.StdEncoding.EncodeToString(key))
}

func TestImportAWX(t *testing.T) {
	setup(t)
	ctx := context.Background()
	a := adminClient(t)
	// The source AWX database lives next to the test database.
	admin, err := pgx.Connect(ctx, databaseURL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("awxsrc_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
	srcURL := databaseURL(name)
	src, err := pgx.Connect(ctx, srcURL)
	if err != nil {
		t.Fatal(err)
	}
	org := uniq("awxorg")
	if _, err := src.Exec(ctx, awxSchema+awxData(org, djangoHash("alicepw"))); err != nil {
		t.Fatal(err)
	}
	src.Close(ctx)

	before := a.get("organizations/").count()
	if _, err := importer.Run(ctx, app, importer.Options{SourceURL: srcURL, SecretKey: "awx-secret-key", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if a.get("organizations/").count() != before {
		t.Fatal("dry run wrote data")
	}
	if _, err := importer.Run(ctx, app, importer.Options{SourceURL: srcURL, SecretKey: "wrong"}); err == nil || !strings.Contains(err.Error(), "SECRET_KEY") {
		t.Fatalf("wrong secret key: %v", err)
	}
	rep, err := importer.Run(ctx, app, importer.Options{SourceURL: srcURL, SecretKey: "awx-secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.delete("settings/ldap/")
		a.patch("settings/authentication/", map[string]any{"SOCIAL_AUTH_TEAM_MAP": nil})
	})
	warnings := strings.Join(rep.Warnings, "\n")
	for _, w := range []string{`user "admin" already exists`, "svn projects are not supported", "slack notifications are not supported",
		`schedule "broken" imported disabled`, "setting RADIUS_SERVER skipped: not supported", "setting AUTH_LDAP_GROUP_TYPE_PARAMS skipped: Invalid key(s): \"colour\"."} {
		if !strings.Contains(warnings, w) {
			t.Errorf("missing warning %q in:\n%s", w, warnings)
		}
	}

	o := a.get("organizations/?name=" + org).results()
	if len(o) != 1 || o[0]["max_hosts"] != float64(5) {
		t.Fatalf("organization: %v", o)
	}
	orgID := int64(o[0]["id"].(float64))
	alice := &client{t: t, user: "alice-" + org, pass: "alicepw"}
	if r := must(t, alice.get("me/"), 200); r.results()[0]["email"] != "alice@example.com" {
		t.Fatalf("alice: %v", r.Body)
	}
	bob := a.get("users/?username=bob-" + org).results()[0]
	if bob["is_system_auditor"] != true {
		t.Fatalf("bob should be an auditor: %v", bob)
	}
	// AWX's LDAP users stay LDAP users; its login settings come along.
	if bob["external_account"] != "ldap" || bob["ldap_dn"] != "uid=bob,ou=people,dc=example,dc=com" {
		t.Errorf("bob is not an LDAP user: %v", bob)
	}
	sam := a.get("users/?username=sam-" + org).results()[0]
	if sam["external_account"] != "enterprise" || fmt.Sprint(sam["auth"]) != "[map[provider:saml uid:okta:sam@example.com]]" {
		t.Errorf("sam is not a SAML user: %v", sam)
	}
	if gina := a.get("users/?username=gina-" + org).results()[0]; gina["external_account"] != "social" ||
		fmt.Sprint(gina["auth"]) != "[map[provider:github-org uid:583231]]" {
		t.Errorf("gina is not a GitHub user: %v", gina)
	}
	ldapSettings := must(t, a.get("settings/ldap/"), 200).Body
	if ldapSettings["AUTH_LDAP_SERVER_URI"] != "ldap://127.0.0.1:1" || ldapSettings["AUTH_LDAP_BIND_PASSWORD"] != "$encrypted$" ||
		fmt.Sprint(ldapSettings["AUTH_LDAP_USER_SEARCH"]) != "[ou=people,dc=example,dc=com SCOPE_SUBTREE (uid=%(user)s)]" ||
		ldapSettings["AUTH_LDAP_START_TLS"] != false || ldapSettings["AUTH_LDAP_ORGANIZATION_MAP"] == nil {
		t.Errorf("ldap settings: %v", ldapSettings)
	}
	if pw, err := app.Crypto.Decrypt(app.Settings.String(ctx, "AUTH_LDAP_BIND_PASSWORD")); err != nil || pw != "svc-pw" {
		t.Errorf("bind password: %q %v", pw, err)
	}
	if m := must(t, a.get("settings/authentication/"), 200).Body["SOCIAL_AUTH_TEAM_MAP"]; m == nil {
		t.Error("SOCIAL_AUTH_TEAM_MAP not imported")
	}
	if must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"] == "https://awx.example.com" {
		t.Error("a system setting was imported")
	}

	if strings.Contains(warnings, `credential "aws lookup" skipped`) {
		t.Errorf("the AWS Secrets Manager lookup is supported:\n%s", warnings)
	}
	creds := a.get(fmt.Sprintf("credentials/?organization=%d&order_by=name", orgID)).results()
	if len(creds) != 2 || creds[0]["name"] != "aws lookup" || creds[1]["name"] != "machine" ||
		creds[0]["credential_type"] != float64(credType(t, a, "aws_secretsmanager_credential")) {
		t.Fatalf("credentials: %v", creds)
	}
	creds = creds[1:]
	resolved, err := resolveCredential(int64(creds[0]["id"].(float64)))
	if err != nil || resolved["password"] != "s3cr3t p@ss" || resolved["username"] != "root" {
		t.Fatalf("credential secret: %v %v", resolved, err)
	}
	if ct := a.get("credential_types/?name=MyCloud-" + org); ct.count() != 1 {
		t.Fatal("custom credential type missing")
	}

	invs := a.get(fmt.Sprintf("inventories/?organization=%d&order_by=name", orgID)).results()
	if len(invs) != 2 || invs[0]["name"] != "inv" || invs[1]["kind"] != "smart" {
		t.Fatalf("inventories: %v", invs)
	}
	invID := int64(invs[0]["id"].(float64))
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", invID))); strings.Join(got, ",") != "db1,web1" {
		t.Fatalf("hosts: %v", got)
	}
	web := a.get(fmt.Sprintf("inventories/%d/groups/?name=web", invID)).results()[0]
	if got := names(a.get(fmt.Sprintf("groups/%d/children/", int64(web["id"].(float64))))); strings.Join(got, ",") != "web-a" {
		t.Fatalf("group children: %v", got)
	}
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", int64(invs[1]["id"].(float64))))); strings.Join(got, ",") != "web1" {
		t.Fatalf("smart inventory hosts: %v", got)
	}

	jt := a.get(fmt.Sprintf("job_templates/?organization=%d", orgID)).results()
	if len(jt) != 1 || jt[0]["playbook"] != "site.yml" || jt[0]["ask_variables_on_launch"] != true || jt[0]["webhook_service"] != "github" {
		t.Fatalf("job templates: %v", jt)
	}
	jtID := int64(jt[0]["id"].(float64))
	if r := a.get(fmt.Sprintf("job_templates/%d/credentials/", jtID)); r.count() != 1 {
		t.Fatal("job template credentials")
	}
	if r := a.get(fmt.Sprintf("job_templates/%d/labels/", jtID)); r.count() != 1 {
		t.Fatal("job template labels")
	}
	survey := a.get(fmt.Sprintf("job_templates/%d/survey_spec/", jtID)).Raw
	if !strings.Contains(survey, `"$encrypted$"`) || strings.Contains(survey, "survey-default") {
		t.Fatalf("survey: %s", survey)
	}
	var stored string
	app.DB.QueryRow(ctx, "SELECT survey_spec->'spec'->0->>'default' FROM unified_job_templates WHERE id = $1", jtID).Scan(&stored)
	if plain, err := app.Crypto.Decrypt(stored); err != nil || plain != "survey-default" {
		t.Fatalf("survey default: %q %v", plain, err)
	}
	if k := a.get(fmt.Sprintf("job_templates/%d/webhook_key/", jtID)).Body["webhook_key"]; k != "hookkey" {
		t.Fatalf("webhook key: %v", k)
	}
	proj := a.get(fmt.Sprintf("projects/?organization=%d", orgID)).results()
	if len(proj) != 1 || proj[0]["scm_url"] != "https://example.com/repo.git" || !strings.HasPrefix(proj[0]["local_path"].(string), "_") {
		t.Fatalf("projects: %v", proj)
	}
	if src := a.get(fmt.Sprintf("inventories/%d/inventory_sources/", invID)).results(); len(src) != 1 || src[0]["source_project"] != proj[0]["id"] {
		t.Fatalf("inventory source: %v", src)
	}

	wf := a.get(fmt.Sprintf("workflow_job_templates/?organization=%d", orgID)).results()
	if len(wf) != 1 {
		t.Fatalf("workflows: %v", wf)
	}
	// Imported rows keep their AWX ids only when free, so nodes are found by identifier.
	nodes := map[string]map[string]any{}
	for _, n := range a.get(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", int64(wf[0]["id"].(float64)))).results() {
		nodes[n["identifier"].(string)] = n
	}
	if first, gate := nodes["first"], nodes["gate"]; len(nodes) != 2 || first == nil || gate == nil || fmt.Sprint(first["success_nodes"]) != fmt.Sprintf("[%v]", gate["id"]) ||
		gate["summary_fields"].(map[string]any)["unified_job_template"].(map[string]any)["unified_job_type"] != "workflow_approval" {
		t.Fatalf("workflow nodes: %v", nodes)
	}

	scheds := a.get(fmt.Sprintf("job_templates/%d/schedules/?order_by=name", jtID)).results()
	if len(scheds) != 2 || scheds[0]["enabled"] != false || scheds[1]["limit"] != "web1" || scheds[1]["next_run"] == nil {
		t.Fatalf("schedules: %v", scheds)
	}
	sjt := a.get("system_job_templates/?job_type=cleanup_jobs").results()[0]
	cs := a.get(fmt.Sprintf("system_job_templates/%d/schedules/", int64(sjt["id"].(float64)))).results()
	if len(cs) != 1 || cs[0]["extra_data"].(map[string]any)["days"] != "30" {
		t.Fatalf("management schedule: %v", cs)
	}

	nts := a.get(fmt.Sprintf("notification_templates/?organization=%d&order_by=name", orgID)).results()
	if len(nts) != 2 || nts[0]["notification_configuration"].(map[string]any)["password"] != "$encrypted$" {
		t.Fatalf("notification templates: %v", nts)
	}
	if mail := nts[1]; mail["notification_type"] != "email" || fmt.Sprint(mail["notification_configuration"]) !=
		"map[host:smtp.example.com password: port:587 recipients:[ops@example.com] sender:awx@example.com timeout:30 use_ssl:false use_tls:true username:]" {
		t.Fatalf("email notification template: %v", mail)
	}
	var pw string
	app.DB.QueryRow(ctx, "SELECT notification_configuration->>'password' FROM notification_templates WHERE id = $1", nts[0]["id"]).Scan(&pw)
	if plain, err := app.Crypto.Decrypt(pw); err != nil || plain != "hook-pass" {
		t.Fatalf("notification password: %q %v", plain, err)
	}
	if r := a.get(fmt.Sprintf("job_templates/%d/notification_templates_success/", jtID)); r.count() != 1 {
		t.Fatal("job template notification link")
	}
	if r := a.get(fmt.Sprintf("organizations/%d/notification_templates_error/", orgID)); r.count() != 1 {
		t.Fatal("organization notification link")
	}

	// Roles: alice administers the organization; bob runs the template through team Ops.
	must(t, alice.patch(fmt.Sprintf("organizations/%d/", orgID), map[string]any{"description": "edited by alice"}), 200)
	teams := a.get(fmt.Sprintf("organizations/%d/teams/", orgID)).results()
	if len(teams) != 1 {
		t.Fatalf("teams: %v", teams)
	}
	teamRoles := a.get(fmt.Sprintf("teams/%d/roles/", int64(teams[0]["id"].(float64)))).Raw
	if !strings.Contains(teamRoles, `"name":"Execute"`) || !strings.Contains(teamRoles, `"name":"Use"`) || !strings.Contains(teamRoles, `"resource_name":"deploy"`) {
		t.Fatalf("team roles: %s", teamRoles)
	}
	if !strings.Contains(a.get(fmt.Sprintf("teams/%d/users/", int64(teams[0]["id"].(float64)))).Raw, "bob-"+org) {
		t.Fatal("bob not in team")
	}
}
