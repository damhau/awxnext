package test

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/credplugins"
)

// lookupValue resolves a secret through a credential of the external type
// ns: a machine credential's password comes from it.
func lookupValue(t *testing.T, a *client, org int64, ns string, inputs, metadata map[string]any) (string, error) {
	t.Helper()
	src := must(t, a.post("credentials/", map[string]any{"name": uniq(ns), "organization": org, "credential_type": credType(t, a, ns), "inputs": inputs}), 201).id()
	target := must(t, a.post("credentials/", map[string]any{"name": uniq("machine"), "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "svc"}}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", target), map[string]any{"source_credential": src, "input_field_name": "password", "metadata": metadata}), 201)
	rc, err := resolveCredential(target)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(rc["password"]), nil
}

// typeTest calls a credential type's test button.
func typeTest(t *testing.T, a *client, ns string, inputs, metadata map[string]any, status int) {
	t.Helper()
	must(t, a.post(fmt.Sprintf("credential_types/%d/test/", credType(t, a, ns)), map[string]any{"inputs": inputs, "metadata": metadata}), status)
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func TestCyberArkCCPLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ca := newTestCA(t)
	_, _, serverPair := ca.issue(t, "ccp", true)
	certPEM, keyPEM, _ := ca.issue(t, "awx-client", false)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "awx-client" {
			jsonReply(w, 403, map[string]any{"ErrorMsg": "client certificate required"})
			return
		}
		want := "AppId=awx-app&Query=Safe%3DTest%3BObject%3Ddb%20admin&QueryFormat=Exact"
		if r.URL.Path != "/AIMWebService/api/Accounts" && r.URL.Path != "/CustomWS/api/Accounts" || r.URL.RawQuery != want && r.URL.RawQuery != want+"&reason=audit" {
			jsonReply(w, 404, map[string]any{"ErrorMsg": "no object " + r.URL.String()})
			return
		}
		jsonReply(w, 200, map[string]any{"Content": "ccp-pass", "UserName": "dbadmin", "Address": "db.example.com"})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.pool}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	in := map[string]any{"url": srv.URL, "app_id": "awx-app", "client_cert": certPEM, "client_key": keyPEM, "verify": false}
	md := map[string]any{"object_query": "Safe=Test;Object=db admin"}
	with := func(m map[string]any, kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	typeTest(t, a, "aim", in, md, 202)
	for prop, want := range map[string]string{"": "ccp-pass", "password": "ccp-pass", "username": "dbadmin", "Address": "db.example.com"} {
		if got, err := lookupValue(t, a, org, "aim", in, with(md, "object_property", prop)); err != nil || got != want {
			t.Errorf("property %q: %q %v", prop, got, err)
		}
	}
	typeTest(t, a, "aim", in, with(md, "object_property", "Nope"), 400)
	typeTest(t, a, "aim", with(in, "webservice_id", "CustomWS"), with(md, "reason", "audit"), 202)
	// Without the client certificate the provider refuses; with verification its certificate is unknown.
	typeTest(t, a, "aim", with(in, "client_cert", "", "client_key", ""), md, 400)
	typeTest(t, a, "aim", with(in, "verify", true), md, 400)
}

func TestConjurLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	token := base64.StdEncoding.EncodeToString([]byte(`{"protected":"x","payload":"y"}`))
	fake := func(prefix string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			switch {
			case r.Method == "POST" && r.RequestURI == prefix+"/authn/myorg/host%2Fapp/authenticate":
				if string(body) != "conjur-api-key" || r.Header.Get("Accept-Encoding") != "base64" {
					w.WriteHeader(401)
					return
				}
				io.WriteString(w, token)
			case r.Method == "GET" && strings.HasPrefix(r.RequestURI, prefix+"/secrets/myorg/variable/db%2Fpassword"):
				if r.Header.Get("Authorization") != `Token token="`+token+`"` {
					w.WriteHeader(401)
					return
				}
				io.WriteString(w, "conjur-pass"+r.URL.Query().Get("version"))
			default:
				w.WriteHeader(404)
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	md := map[string]any{"secret_path": "db/password"}
	for _, prefix := range []string{"", "/api"} { // Conjur, then Conjur Cloud's /api paths
		srv := fake(prefix)
		in := map[string]any{"url": srv.URL, "api_key": "conjur-api-key", "account": "myorg", "username": "host/app"}
		typeTest(t, a, "conjur", in, md, 202)
		if got, err := lookupValue(t, a, org, "conjur", in, md); err != nil || got != "conjur-pass" {
			t.Errorf("prefix %q: %q %v", prefix, got, err)
		}
		if got, _ := lookupValue(t, a, org, "conjur", in, map[string]any{"secret_path": "db/password", "secret_version": "3"}); got != "conjur-pass3" {
			t.Errorf("version: %q", got)
		}
		typeTest(t, a, "conjur", map[string]any{"url": srv.URL, "api_key": "wrong", "account": "myorg", "username": "host/app"}, md, 400)
	}
}

func TestCentrifyLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path == "/oauth2/token/awx" {
			u, p, _ := r.BasicAuth()
			r.ParseForm()
			if u != "api@example" || p != "api-pass" || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "awx" {
				w.WriteHeader(401)
				return
			}
			jsonReply(w, 200, map[string]any{"access_token": "centrify-token"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer centrify-token" || r.Header.Get("X-CENTRIFY-NATIVE-CLIENT") != "true" {
			w.WriteHeader(401)
			return
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/Redrock/query":
			rows := []any{}
			if body["Script"] == "Select ID from VaultAccount where  Name='web01' and User='root'" {
				rows = append(rows, map[string]any{"Row": map[string]any{"ID": "acct-1"}})
			}
			jsonReply(w, 200, map[string]any{"Result": map[string]any{"Results": rows}})
		case "/ServerManage/CheckoutPassword":
			if body["ID"] != "acct-1" {
				jsonReply(w, 200, map[string]any{"Result": map[string]any{}})
				return
			}
			jsonReply(w, 200, map[string]any{"Result": map[string]any{"Password": "centrify-pass"}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	in := map[string]any{"url": srv.URL, "client_id": "api@example", "client_password": "api-pass"}
	md := map[string]any{"account-name": "root", "system-name": "web01"}
	typeTest(t, a, "centrify_vault_kv", in, md, 202)
	if got, err := lookupValue(t, a, org, "centrify_vault_kv", in, md); err != nil || got != "centrify-pass" {
		t.Fatalf("%q %v", got, err)
	}
	typeTest(t, a, "centrify_vault_kv", in, map[string]any{"account-name": "root", "system-name": "db01"}, 400)
	typeTest(t, a, "centrify_vault_kv", map[string]any{"url": srv.URL, "client_id": "api@example", "client_password": "x"}, md, 400)
}

func TestThycoticDSVLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	key := base64.StdEncoding.EncodeToString([]byte("-----BEGIN KEY-----\nabc\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/ex.eu/v1/token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["client_id"] != "dsv-id" || body["client_secret"] != "dsv-secret" || body["grant_type"] != "client_credentials" {
				jsonReply(w, 401, map[string]any{"message": "bad client"})
				return
			}
			jsonReply(w, 200, map[string]any{"accessToken": "dsv-token", "tokenType": "bearer"})
		case r.Method == "GET" && r.URL.Path == "/ex.eu/v1/secrets/app/db":
			if r.Header.Get("Authorization") != "Bearer dsv-token" {
				w.WriteHeader(401)
				return
			}
			jsonReply(w, 200, map[string]any{"path": "app:db", "data": map[string]any{"password": "dsv-pass", "key": key}})
		default:
			jsonReply(w, 404, map[string]any{"message": "not found"})
		}
	}))
	t.Cleanup(srv.Close)
	credplugins.DSVURLTemplate = srv.URL + "/%s.%s"
	t.Cleanup(func() { credplugins.DSVURLTemplate = "https://%s.secretsvaultcloud.%s" })
	in := map[string]any{"tenant": "ex", "tld": "eu", "client_id": "dsv-id", "client_secret": "dsv-secret"}
	md := map[string]any{"path": "/app/db", "secret_field": "password", "secret_decoding": "No Decoding"}
	typeTest(t, a, "thycotic_dsv", in, md, 202)
	if got, err := lookupValue(t, a, org, "thycotic_dsv", in, md); err != nil || got != "dsv-pass" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := lookupValue(t, a, org, "thycotic_dsv", in, map[string]any{"path": "/app/db", "secret_field": "key", "secret_decoding": "Decode Base64"}); got != "-----BEGIN KEY-----\nabc\n" {
		t.Fatalf("decoded: %q", got)
	}
	typeTest(t, a, "thycotic_dsv", in, map[string]any{"path": "/app/db", "secret_field": "nope", "secret_decoding": "No Decoding"}, 400)
	typeTest(t, a, "thycotic_dsv", map[string]any{"tenant": "ex", "tld": "eu", "client_id": "dsv-id", "client_secret": "x"}, md, 400)
}

func TestThycoticSecretServerLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/SecretServer/oauth2/token":
			r.ParseForm()
			if r.Form.Get("username") != "app-user" || r.Form.Get("password") != "app-pass" || r.Form.Get("grant_type") != "password" ||
				r.Form.Has("domain") && r.Form.Get("domain") != "CORP" {
				jsonReply(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
			jsonReply(w, 200, map[string]any{"access_token": "tss-token-" + r.Form.Get("domain"), "token_type": "bearer", "expires_in": 1200})
		case !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tss-token-"):
			w.WriteHeader(401)
		case r.URL.Path == "/SecretServer/api/v1/secrets/42":
			jsonReply(w, 200, map[string]any{"id": 42, "name": "db", "items": []any{
				map[string]any{"slug": "password", "itemValue": "tss-pass-" + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tss-token-"), "fileAttachmentId": nil},
				map[string]any{"slug": "private-key", "itemValue": "*** Not Valid For Display ***", "fileAttachmentId": 7},
			}})
		case r.URL.Path == "/SecretServer/api/v1/secrets/42/fields/private-key":
			io.WriteString(w, "KEY CONTENT")
		default:
			jsonReply(w, 404, map[string]any{"message": "not found"})
		}
	}))
	t.Cleanup(srv.Close)
	in := map[string]any{"server_url": srv.URL + "/SecretServer", "username": "app-user", "password": "app-pass"}
	md := map[string]any{"secret_id": "42", "secret_field": "password"}
	typeTest(t, a, "thycotic_tss", in, md, 202)
	if got, err := lookupValue(t, a, org, "thycotic_tss", in, md); err != nil || got != "tss-pass-" {
		t.Fatalf("%q %v", got, err)
	}
	withDomain := map[string]any{"server_url": srv.URL + "/SecretServer", "username": "app-user", "password": "app-pass", "domain": "CORP"}
	if got, _ := lookupValue(t, a, org, "thycotic_tss", withDomain, md); got != "tss-pass-CORP" {
		t.Fatalf("domain: %q", got)
	}
	if got, _ := lookupValue(t, a, org, "thycotic_tss", in, map[string]any{"secret_id": "42", "secret_field": "private-key"}); got != "KEY CONTENT" {
		t.Fatalf("file field: %q", got)
	}
	typeTest(t, a, "thycotic_tss", in, map[string]any{"secret_id": "43", "secret_field": "password"}, 400)
	typeTest(t, a, "thycotic_tss", map[string]any{"server_url": srv.URL + "/SecretServer", "username": "app-user", "password": "x"}, md, 400)
}

func TestAWSSecretsManagerLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIATEST/") || !strings.Contains(auth, "/eu-west-1/secretsmanager/aws4_request") {
			w.Header().Set("X-Amzn-ErrorType", "UnrecognizedClientException")
			jsonReply(w, 400, map[string]any{"__type": "UnrecognizedClientException", "message": "The security token included in the request is invalid."})
			return
		}
		if r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			w.WriteHeader(400)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch body["SecretId"] {
		case "prod/db":
			json.NewEncoder(w).Encode(map[string]any{"Name": "prod/db", "SecretString": "aws-pass"})
		case "prod/blob":
			json.NewEncoder(w).Encode(map[string]any{"Name": "prod/blob", "SecretBinary": base64.StdEncoding.EncodeToString([]byte("binary-pass"))})
		default:
			w.Header().Set("X-Amzn-ErrorType", "ResourceNotFoundException")
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"__type": "ResourceNotFoundException", "message": "Secrets Manager can't find the specified secret."})
		}
	}))
	t.Cleanup(srv.Close)
	credplugins.AWSEndpoint = srv.URL
	t.Cleanup(func() { credplugins.AWSEndpoint = "" })
	in := map[string]any{"aws_access_key": "AKIATEST", "aws_secret_key": "secret"}
	md := map[string]any{"region_name": "eu-west-1", "secret_name": "prod/db"}
	typeTest(t, a, "aws_secretsmanager_credential", in, md, 202)
	if got, err := lookupValue(t, a, org, "aws_secretsmanager_credential", in, md); err != nil || got != "aws-pass" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := lookupValue(t, a, org, "aws_secretsmanager_credential", in, map[string]any{"region_name": "eu-west-1", "secret_name": "prod/blob"}); got != "binary-pass" {
		t.Fatalf("binary: %q", got)
	}
	typeTest(t, a, "aws_secretsmanager_credential", in, map[string]any{"region_name": "eu-west-1", "secret_name": "prod/none"}, 400)
	typeTest(t, a, "aws_secretsmanager_credential", map[string]any{"aws_access_key": "AKIAOTHER", "aws_secret_key": "secret"}, md, 400)
}
