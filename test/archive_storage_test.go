package test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/damhau/awxnext/internal/blob"
)

// Azurite's well-known development account.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

// TestEventArchiveS3 archives job events in MinIO, an S3-compatible server
// (CI starts pgsty/minio; AWXNEXT_TEST_S3_ENDPOINT, _ACCESS_KEY and
// _SECRET_KEY), signing in with an Amazon Web Services credential.
func TestEventArchiveS3(t *testing.T) {
	endpoint := os.Getenv("AWXNEXT_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("AWXNEXT_TEST_S3_ENDPOINT is not set (a MinIO server)")
	}
	access, secret := os.Getenv("AWXNEXT_TEST_S3_ACCESS_KEY"), os.Getenv("AWXNEXT_TEST_S3_SECRET_KEY")
	f := newJobFixture(t)
	a := f.a
	bucket := strings.ToLower(uniq("archives"))
	client := s3.New(s3.Options{BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: access, SecretAccessKey: secret}, nil
		})})
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	awsType := must(t, a.get("credential_types/?namespace=aws"), 200).results()[0]["id"]
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("archive-aws"), "organization": f.org, "credential_type": awsType,
		"inputs": map[string]any{"username": access, "password": secret}}), 201).id()
	// The credential must be an AWS or Azure Resource Manager one.
	machine := must(t, a.get("credential_types/?namespace=ssh"), 200).results()[0]["id"]
	other := must(t, a.post("credentials/", map[string]any{"name": uniq("machine"), "organization": f.org, "credential_type": machine,
		"inputs": map[string]any{"username": "u"}}), 201).id()
	if r := must(t, a.patch("settings/jobs/", map[string]any{"JOB_EVENTS_ARCHIVE_CREDENTIAL": other}), 400); !strings.Contains(fmt.Sprint(r.Body), "Amazon Web Services") {
		t.Fatalf("a machine credential: %v", r.Body)
	}
	store, err := blob.NewS3(context.Background(), blob.S3Config{Bucket: bucket, Endpoint: endpoint, AccessKey: access, SecretKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	archiveInStore(t, f, "s3", store, map[string]any{"JOB_EVENTS_ARCHIVE_STORAGE": "s3", "JOB_EVENTS_ARCHIVE_BUCKET": bucket,
		"JOB_EVENTS_ARCHIVE_ENDPOINT": endpoint, "JOB_EVENTS_ARCHIVE_CREDENTIAL": cred}, bucket)
}

// TestEventArchiveAzureBlob archives job events in Azurite (CI starts it;
// AWXNEXT_TEST_AZURITE_BLOB_ENDPOINT is its account's blob endpoint),
// signing in from the environment (AZURE_STORAGE_CONNECTION_STRING).
func TestEventArchiveAzureBlob(t *testing.T) {
	endpoint := strings.TrimRight(os.Getenv("AWXNEXT_TEST_AZURITE_BLOB_ENDPOINT"), "/")
	if endpoint == "" {
		t.Skip("AWXNEXT_TEST_AZURITE_BLOB_ENDPOINT is not set (an Azurite server)")
	}
	conn := fmt.Sprintf("DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=%s;BlobEndpoint=%s;", azuriteKey, endpoint)
	t.Setenv("AZURE_STORAGE_CONNECTION_STRING", conn)
	f := newJobFixture(t)
	container := strings.ToLower(uniq("archives"))
	client, err := azblob.NewClientFromConnectionString(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateContainer(context.Background(), container, nil); err != nil {
		t.Fatal(err)
	}
	store, err := blob.NewAzure(context.Background(), blob.AzureConfig{Container: container})
	if err != nil {
		t.Fatal(err)
	}
	archiveInStore(t, f, "azure_blob", store, map[string]any{"JOB_EVENTS_ARCHIVE_STORAGE": "azure_blob", "JOB_EVENTS_ARCHIVE_BUCKET": container}, container)
	if _, err := store.Get(context.Background(), container+"/missing"); !bloberror.HasCode(err, bloberror.BlobNotFound) {
		t.Fatalf("a missing blob: %v", err)
	}
}

// archiveInStore: with the archive settings, a finished job's events are
// archived in store (the database keeps where, not the data), read back
// when its output is viewed, and the object is deleted after the job.
func archiveInStore(t *testing.T, f *jobFixture, kind string, store blob.Store, settings map[string]any, bucket string) {
	t.Helper()
	a := f.a
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := app.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// A job archived in the database before the change stays readable.
	jt := f.template(t, "site.yml", nil)
	old := f.launch(t, a, jt, nil)
	mustStatus(t, f, old, "successful")
	exec("UPDATE unified_jobs SET created = now() - interval '30 days' WHERE id = $1", old.id())
	exec("UPDATE job_events SET job_created = now() - interval '30 days' WHERE job_id = $1", old.id())
	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}

	must(t, a.patch("settings/jobs/", settings), 200)
	t.Cleanup(func() {
		a.patch("settings/jobs/", map[string]any{"JOB_EVENTS_ARCHIVE_STORAGE": "database", "JOB_EVENTS_ARCHIVE_BUCKET": "",
			"JOB_EVENTS_ARCHIVE_ENDPOINT": "", "JOB_EVENTS_ARCHIVE_CREDENTIAL": nil})
	})
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	events := a.get(fmt.Sprintf("jobs/%d/job_events/?page_size=1", job.id())).count()
	exec("UPDATE unified_jobs SET created = now() - interval '30 days' WHERE id = $1", job.id())
	exec("UPDATE job_events SET job_created = now() - interval '30 days' WHERE job_id = $1", job.id())
	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	var storage, location, install string
	var inDB bool
	if err := app.DB.QueryRow(ctx, "SELECT storage, location, data IS NOT NULL FROM job_event_archives WHERE job_id = $1", job.id()).Scan(&storage, &location, &inDB); err != nil {
		t.Fatal(err)
	}
	app.DB.QueryRow(ctx, "SELECT value #>> '{}' FROM settings WHERE key = 'INSTALL_UUID'").Scan(&install)
	if want := fmt.Sprintf("%s/job-events/%s/%d.jsonl.gz", bucket, install, job.id()); storage != kind || location != want || inDB {
		t.Fatalf("archive row: storage %q location %q (want %q) data in the database %v", storage, location, want, inDB)
	}
	data, err := store.Get(ctx, location)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	lines, _ := io.ReadAll(zr)
	if n := bytes.Count(lines, []byte("\n")); n != events {
		t.Fatalf("archive in %s: %d events, the job had %d", kind, n, events)
	}

	// Viewing the output reads the archive back.
	if out := f.stdout(t, job.id()); !strings.Contains(out, "hello nobody from h1") {
		t.Fatalf("restored stdout:\n%s", out)
	}
	if r := a.get(fmt.Sprintf("jobs/%d/job_events/?page_size=1", job.id())); r.count() != events {
		t.Fatalf("restored events: %d, want %d", r.count(), events)
	}
	if out := f.stdout(t, old.id()); !strings.Contains(out, "hello nobody from h1") {
		t.Fatalf("archive kept in the database, after the change:\n%s", out)
	}

	// Deleting the job deletes its object at the next archive run.
	must(t, a.delete(fmt.Sprintf("jobs/%d/", job.id())), 204)
	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, location); err == nil {
		t.Fatalf("%s still holds %s after the job was deleted", kind, location)
	}
	var pending int
	app.DB.QueryRow(ctx, "SELECT count(*) FROM job_event_archive_deletions WHERE location = $1", location).Scan(&pending)
	if pending != 0 {
		t.Fatal("the deletion is still queued")
	}
}
