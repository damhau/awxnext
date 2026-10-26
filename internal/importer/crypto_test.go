package importer

import "testing"

// Vectors produced by AWX's encrypt_field (Fernet256, SECRET_KEY "awx-secret-key").
const (
	vecCredential   = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnEzN2RVVUZyN0FpRVJqX3ZoODJUaVdGYTlWaTZIcklNMF9keDVWWFlQMk1mVEl3QU1WUFpTR2dVRUNZWXE0amx5RUMyMzQwa1dKY04tZVNpeDRvdEdPVzJ5TkE9PQ=="
	vecSurvey       = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnEzN2RWREdoTnlEZFZRWU5rOUtsOVIzR3ZneV9wQmpmbU5QdnRLUUIwWWVjdG1KWlZfM0N6d3dRNFU3ODVqbGZlRVp4aW5ENl9XUnltcUdLbHUyRFEzVmFHNEE9PQ=="
	vecNotification = "$encrypted$UTF8$AESCBC$Z0FBQUFBQnEzN2RWcVBOQ2lxODhwMHRjaTdSTDNyUDNxWmxDbU1kNHgzWGtLMDlySkh3R2ZjWnhFYlF1VzVZVVJMc1FFM25tNTN2UzdwSHdyUUs1Y3RoeDdBX0lHUUZIcUE9PQ=="
)

func TestDecryptAWX(t *testing.T) {
	pk := int64(42)
	got, err := DecryptAWX("awx-secret-key", &pk, "password", vecCredential)
	if err != nil || got != "s3cr3t p@ss" {
		t.Fatalf("credential: %q %v", got, err)
	}
	if got, err := DecryptAWX("awx-secret-key", nil, "value", vecSurvey); err != nil || got != "survey-default" {
		t.Fatalf("survey: %q %v", got, err)
	}
	nt := int64(7)
	if got, err := DecryptAWX("awx-secret-key", &nt, "notification_configuration", vecNotification); err != nil || got != "hook-pass" {
		t.Fatalf("notification: %q %v", got, err)
	}
	if _, err := DecryptAWX("wrong-key", &pk, "password", vecCredential); err == nil {
		t.Fatal("wrong key accepted")
	}
	other := int64(43)
	if _, err := DecryptAWX("awx-secret-key", &other, "password", vecCredential); err == nil {
		t.Fatal("wrong pk accepted")
	}
	if got, _ := DecryptAWX("k", nil, "f", "plain"); got != "plain" {
		t.Fatal("plain values pass through")
	}
}
