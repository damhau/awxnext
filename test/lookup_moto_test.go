package test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/damhau/awxnext/internal/credplugins"
)

// TestAWSSecretsManagerMoto runs the AWS Secrets Manager lookup against moto
// with its authentication on: moto checks the access key, the signature and
// the user's policy, as AWS does. CI starts motoserver/moto, and
// AWXNEXT_TEST_MOTO_ENDPOINT is its URL, with an IP address.
func TestAWSSecretsManagerMoto(t *testing.T) {
	endpoint := strings.TrimRight(os.Getenv("AWXNEXT_TEST_MOTO_ENDPOINT"), "/")
	if endpoint == "" {
		t.Skip("AWXNEXT_TEST_MOTO_ENDPOINT is not set (a moto server)")
	}
	key, secret := motoSecrets(t, endpoint)
	a := adminClient(t)
	org := newOrg(t, a)
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{u.Host}
	// Behind a proxy on loopback, lookups connect to the proxy instead.
	if p, _ := http.ProxyFromEnvironment(&http.Request{URL: u}); p != nil {
		allowed = append(allowed, p.Host)
	}
	setJobs(t, a, credplugins.LocalAddressesSetting, allowed)
	credplugins.AWSEndpoint = endpoint
	t.Cleanup(func() { credplugins.AWSEndpoint = "" })

	const ns = "aws_secretsmanager_credential"
	in := map[string]any{"aws_access_key": key, "aws_secret_key": secret}
	md := func(region, name string) map[string]any {
		return map[string]any{"region_name": region, "secret_name": name}
	}
	lookup := must(t, a.post("credentials/", map[string]any{"name": uniq("aws"), "organization": org, "credential_type": credType(t, a, ns), "inputs": in}), 201).id()

	for name, want := range map[string]string{"prod/db": "aws-pass", "prod/blob": "binary-pass"} {
		if got, err := lookupValue(t, a, org, ns, in, md("eu-west-1", name)); err != nil || got != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
		typeTest(t, a, ns, in, md("eu-west-1", name), 202)
		must(t, a.post(fmt.Sprintf("credentials/%d/test/", lookup), map[string]any{"metadata": md("eu-west-1", name)}), 202)
	}

	fails := func(what string, inputs, metadata map[string]any, awsError string) {
		t.Helper()
		_, err := lookupValue(t, a, org, ns, inputs, metadata)
		if err == nil || !strings.Contains(err.Error(), awsError) {
			t.Fatalf("%s: lookup error %v, want %s", what, err, awsError)
		}
		if r := typeTest(t, a, ns, inputs, metadata, 400); !strings.Contains(r.Raw, awsError) {
			t.Fatalf("%s: type test %s", what, r.Raw)
		}
		cred := must(t, a.post("credentials/", map[string]any{"name": uniq("aws"), "organization": org, "credential_type": credType(t, a, ns), "inputs": inputs}), 201).id()
		if r := must(t, a.post(fmt.Sprintf("credentials/%d/test/", cred), map[string]any{"metadata": metadata}), 400); !strings.Contains(r.Raw, awsError) {
			t.Fatalf("%s: credential test %s", what, r.Raw)
		}
	}
	fails("unknown secret", in, md("eu-west-1", "prod/none"), "ResourceNotFoundException")
	fails("another region", in, md("us-east-1", "prod/db"), "ResourceNotFoundException")
	// Moto refuses credentials with an XML error, which Secrets Manager's
	// JSON protocol doesn't decode (AWS answers InvalidSignatureException or
	// UnrecognizedClientException): the SDK's error gives the status.
	fails("wrong secret key", map[string]any{"aws_access_key": key, "aws_secret_key": secret + "x"}, md("eu-west-1", "prod/db"), "StatusCode: 403")
	fails("unknown access key", map[string]any{"aws_access_key": "AKIAUNKNOWN000000000", "aws_secret_key": secret}, md("eu-west-1", "prod/db"), "StatusCode: 403")
}

// motoSecrets resets moto, makes an IAM user allowed to read and create
// secrets, and with its access key the string secret prod/db and the
// binary prod/blob in eu-west-1. It returns the user's access key.
func motoSecrets(t *testing.T, endpoint string) (string, string) {
	t.Helper()
	ctx := context.Background()
	post := func(path, body string) {
		t.Helper()
		r, err := http.Post(endpoint+path, "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s: %s", path, r.Status)
		}
	}
	// Moto lets the first requests after reset-auth in without credentials,
	// enough to make the user; every later one is authenticated.
	post("/moto-api/reset", "")
	post("/moto-api/reset-auth", "3")

	setup := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint),
		Credentials: credentials.NewStaticCredentialsProvider("AKIAMOTOSETUP0000000", "setup", "")})
	user := aws.String("awxnext-lookup")
	if _, err := setup.CreateUser(ctx, &iam.CreateUserInput{UserName: user}); err != nil {
		t.Fatal(err)
	}
	policy := `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": ["secretsmanager:GetSecretValue", "secretsmanager:CreateSecret"], "Resource": "*"}]}`
	if _, err := setup.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: user, PolicyName: aws.String("secrets"), PolicyDocument: aws.String(policy)}); err != nil {
		t.Fatal(err)
	}
	k, err := setup.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: user})
	if err != nil {
		t.Fatal(err)
	}
	key, secret := *k.AccessKey.AccessKeyId, *k.AccessKey.SecretAccessKey

	sm := secretsmanager.New(secretsmanager.Options{Region: "eu-west-1", BaseEndpoint: aws.String(endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(key, secret, "")})
	if _, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String("prod/db"), SecretString: aws.String("aws-pass")}); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String("prod/blob"), SecretBinary: []byte("binary-pass")}); err != nil {
		t.Fatal(err)
	}
	return key, secret
}
