package auth

import (
	"context"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// OIDC implements OpenID Connect login and bearer JWT validation.
// Work in progress: browser flow, JWT validation and group mapping are not implemented yet.
type OIDC struct {
	app *api.App
}

func NewOIDC(app *api.App) *OIDC { return &OIDC{app: app} }

// Enabled reports whether an OIDC provider is configured.
func (o *OIDC) Enabled(ctx context.Context) bool { return false }

// Routes registers the SSO endpoints.
func (o *OIDC) Routes() {}

// AuthenticateJWT validates a provider-issued bearer token.
func (o *OIDC) AuthenticateJWT(ctx context.Context, token string) (*rbac.Principal, error) {
	return nil, api.Detail(401, "OIDC is not configured.")
}
