package test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCA issues certificates for TLS tests.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), pool: pool}
}

// issue signs a server (for 127.0.0.1) or client certificate, as PEM and as a tls.Certificate.
func (ca *testCA) issue(t *testing.T, cn string, server bool) (certPEM, keyPEM string, pair tls.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	pair, err = tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, pair
}

// fakeCertVault is a Vault over TLS with cert auth (at auth/cert and
// auth/tls: the client certificate must be the CA's, CN awx-client, and the
// role, when named, "web"), userpass auth (alice/wonderland), KV v2 and SSH signing.
type fakeCertVault struct {
	url    string
	mu     sync.Mutex
	logins []string
}

func startCertVault(t *testing.T, ca *testCA) *fakeCertVault {
	t.Helper()
	_, _, serverPair := ca.issue(t, "vault", true)
	v := &fakeCertVault{}
	reply := func(w http.ResponseWriter, status int, body any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == "POST" && (r.URL.Path == "/v1/auth/cert/login" || r.URL.Path == "/v1/auth/tls/login"):
			if len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "awx-client" {
				reply(w, 400, map[string]any{"errors": []string{"invalid certificate or no client certificate supplied"}})
				return
			}
			role, _ := body["name"].(string)
			if role != "" && role != "web" {
				reply(w, 400, map[string]any{"errors": []string{"invalid certificate for role " + role}})
				return
			}
			v.mu.Lock()
			v.logins = append(v.logins, r.URL.Path+" role="+role)
			v.mu.Unlock()
			reply(w, 200, map[string]any{"auth": map[string]any{"client_token": "cert-token"}})
		case r.Method == "POST" && r.URL.Path == "/v1/auth/userpass/login/alice":
			if body["password"] != "wonderland" {
				reply(w, 400, map[string]any{"errors": []string{"invalid username or password"}})
				return
			}
			reply(w, 200, map[string]any{"auth": map[string]any{"client_token": "user-token"}})
		case r.Header.Get("X-Vault-Token") != "cert-token" && r.Header.Get("X-Vault-Token") != "user-token":
			reply(w, 403, map[string]any{"errors": []string{"permission denied"}})
		case r.URL.Path == "/v1/kv/data/apps/checkout":
			reply(w, 200, map[string]any{"data": map[string]any{"data": map[string]any{"db_password": "from-" + r.Header.Get("X-Vault-Token")}}})
		case r.URL.Path == "/v1/ssh/sign/ops":
			reply(w, 200, map[string]any{"data": map[string]any{"signed_key": "ssh-ed25519-cert-v01@openssh.com AAAA signed"}})
		default:
			reply(w, 404, map[string]any{"errors": []string{}})
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: ca.pool}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	v.url = srv.URL
	return v
}

// The HashiCorp Vault lookups log in with a TLS client certificate
// (client_cert_public, client_cert_private, client_cert_role) or a username
// and password, as AWX's.
func TestVaultCertAuth(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ca := newTestCA(t)
	vault := startCertVault(t, ca)
	certPEM, keyPEM, _ := ca.issue(t, "awx-client", false)
	otherPEM, otherKey, _ := newTestCA(t).issue(t, "awx-client", false)

	// The inputs are AWX 24.6's, in its order.
	kv := credType(t, a, "hashivault_kv")
	var ids []string
	for _, f := range a.get(fmt.Sprintf("credential_types/%d/", kv)).Body["inputs"].(map[string]any)["fields"].([]any) {
		ids = append(ids, f.(map[string]any)["id"].(string))
	}
	if got := strings.Join(ids, ","); got != "url,token,cacert,role_id,secret_id,client_cert_public,client_cert_private,client_cert_role,namespace,kubernetes_role,username,password,default_auth_path,api_version" {
		t.Fatalf("hashivault_kv inputs: %s", got)
	}

	test := func(inputs map[string]any, metadata map[string]any, status int) resp {
		t.Helper()
		in := map[string]any{"url": vault.url, "cacert": ca.pem, "api_version": "v2"}
		for k, v := range inputs {
			in[k] = v
		}
		md := map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}
		for k, v := range metadata {
			md[k] = v
		}
		return must(t, a.post(fmt.Sprintf("credential_types/%d/test/", kv), map[string]any{"inputs": in, "metadata": md}), status)
	}
	cert := map[string]any{"client_cert_public": certPEM, "client_cert_private": keyPEM, "client_cert_role": "web"}
	test(cert, nil, 202)
	// Without a role Vault picks it from the certificate; a role it refuses, a
	// certificate of another CA or half a certificate do not log in.
	test(map[string]any{"client_cert_public": certPEM, "client_cert_private": keyPEM}, nil, 202)
	test(map[string]any{"client_cert_public": certPEM, "client_cert_private": keyPEM, "client_cert_role": "db"}, nil, 400)
	test(map[string]any{"client_cert_public": otherPEM, "client_cert_private": otherKey}, nil, 400)
	if r := test(map[string]any{"client_cert_public": certPEM}, nil, 400); !strings.Contains(r.Raw, "Token, Username/Password, AppRole, Kubernetes, or TLS authentication parameters must be set") {
		t.Fatalf("half a certificate: %s", r.Raw)
	}
	// The auth path from the metadata (or default_auth_path) picks the mount.
	test(cert, map[string]any{"auth_path": "tls"}, 202)
	vault.mu.Lock()
	logins := strings.Join(vault.logins, "|")
	vault.mu.Unlock()
	if logins != "/v1/auth/cert/login role=web|/v1/auth/cert/login role=|/v1/auth/tls/login role=web" {
		t.Fatalf("logins: %s", logins)
	}
	// Username and password.
	test(map[string]any{"username": "alice", "password": "wonderland"}, nil, 202)
	test(map[string]any{"username": "alice", "password": "nope"}, nil, 400)

	// A credential resolves through it; the key is stored encrypted and not shown.
	lookup := must(t, a.post("credentials/", map[string]any{"name": uniq("vault cert"), "organization": org, "credential_type": kv,
		"inputs": map[string]any{"url": vault.url, "cacert": ca.pem, "api_version": "v2", "client_cert_public": certPEM, "client_cert_private": keyPEM, "client_cert_role": "web"}}), 201)
	if in := lookup.Body["inputs"].(map[string]any); in["client_cert_private"] != "$encrypted$" || in["client_cert_public"] != certPEM {
		t.Fatalf("inputs: %v", in)
	}
	target := must(t, a.post("credentials/", map[string]any{"name": uniq("db"), "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "postgres"}}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", target), map[string]any{"source_credential": lookup.id(), "input_field_name": "password",
		"metadata": map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}}), 201)
	if rc, err := resolveCredential(target); err != nil || rc["password"] != "from-cert-token" {
		t.Fatalf("resolved: %v %v", rc, err)
	}

	// The signed SSH lookup logs in the same way.
	must(t, a.post(fmt.Sprintf("credential_types/%d/test/", credType(t, a, "hashivault_ssh")), map[string]any{
		"inputs":   map[string]any{"url": vault.url, "cacert": ca.pem, "client_cert_public": certPEM, "client_cert_private": keyPEM},
		"metadata": map[string]any{"public_key": "ssh-ed25519 AAAA", "secret_path": "ssh", "role": "ops"},
	}), 202)
}
