package resources

import (
	"context"
	"fmt"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/blob"
)

// archiveStore opens the job event archive storage of a kind from the
// JOB_EVENTS_ARCHIVE_* settings, signed in with the credential they name or
// from the environment.
func archiveStore(app *api.App) func(ctx context.Context, kind string) (blob.Store, error) {
	return func(ctx context.Context, kind string) (blob.Store, error) {
		s := app.Settings
		var cred *ResolvedCredential
		if id, ok := api.ID(s.Get(ctx, "JOB_EVENTS_ARCHIVE_CREDENTIAL")); ok && id > 0 {
			var err error
			if cred, err = ResolveCredential(ctx, app, id); err != nil {
				return nil, err
			}
		}
		in := func(k string) string {
			if cred == nil {
				return ""
			}
			v, _ := cred.Inputs[k].(string)
			return v
		}
		bucket, endpoint := s.String(ctx, "JOB_EVENTS_ARCHIVE_BUCKET"), s.String(ctx, "JOB_EVENTS_ARCHIVE_ENDPOINT")
		switch kind {
		case "s3":
			if cred != nil && cred.Namespace != "aws" {
				return nil, fmt.Errorf("credential %q is not an Amazon Web Services credential", cred.Name)
			}
			return blob.NewS3(ctx, blob.S3Config{Bucket: bucket, Endpoint: endpoint, Region: s.String(ctx, "JOB_EVENTS_ARCHIVE_REGION"),
				AccessKey: in("username"), SecretKey: in("password"), SessionToken: in("security_token")})
		case "azure_blob":
			if cred != nil && cred.Namespace != "azure_rm" {
				return nil, fmt.Errorf("credential %q is not a Microsoft Azure Resource Manager credential", cred.Name)
			}
			return blob.NewAzure(ctx, blob.AzureConfig{Container: bucket, URL: endpoint, Tenant: in("tenant"), Client: in("client"), Secret: in("secret")})
		}
		return nil, fmt.Errorf("unknown job event archive storage %q", kind)
	}
}
