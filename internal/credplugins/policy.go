package credplugins

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/damhau/awxnext/internal/netguard"
)

// The settings that let lookups go further than the user's own inputs.
const (
	// IdentityHostsSetting lists the hosts a lookup may authenticate to as
	// awxnext itself: a HashiCorp Vault with Kubernetes auth (the pod's
	// service account token), an Azure Key Vault without a client secret
	// (workload or managed identity). Empty: never.
	IdentityHostsSetting = "CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS"
	// LocalAddressesSetting lists the loopback and link-local addresses
	// lookups may connect to. Empty: none, nor the cloud metadata service.
	LocalAddressesSetting = "CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES"
)

// Policy is what an administrator allows lookups beyond the user's inputs.
// The zero Policy allows nothing more: no lookup uses awxnext's identity or
// reaches a loopback, link-local or cloud metadata address.
type Policy struct {
	// IdentityHosts may receive awxnext's own identity.
	IdentityHosts netguard.Hosts
	// LocalAddresses are the loopback and link-local addresses allowed.
	LocalAddresses netguard.Allowed
}

// NewPolicy parses the two settings' entries, skipping invalid ones (the
// settings API refuses them when they are saved).
func NewPolicy(identityHosts, localAddresses []string) Policy {
	hosts, _ := netguard.ParseHosts(identityHosts)
	addrs, _ := netguard.ParseAllowed(localAddresses)
	return Policy{IdentityHosts: hosts, LocalAddresses: addrs}
}

func (p Policy) guard() netguard.Guard {
	return netguard.Guard{Allowed: p.LocalAddresses, Setting: LocalAddressesSetting}
}

// checkURL refuses rawURL before any request when its host is a refused
// address written as such, and, when the lookup would send awxnext's own
// identity (what says how), when its host is not listed.
func (p Policy) checkURL(rawURL string, identity string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if err := p.guard().CheckURL(u); err != nil {
		return err
	}
	if identity != "" && !p.IdentityHosts.Match(u) {
		return identityError(identity, u)
	}
	return nil
}

func identityError(how string, u *url.URL) error {
	return fmt.Errorf("%s, which lookups may send only to hosts an administrator lists in %s, and %s is not listed", how, IdentityHostsSetting, netguard.Origin(u))
}

// transport guards a lookup's connections: every one is checked against the
// refused addresses, and when the lookup authenticates as awxnext (identity
// says how), every request's URL, redirects included, must be listed in
// IdentityHosts or in also.
func (p Policy) transport(identity string, also netguard.Hosts) func(*http.Transport) http.RoundTripper {
	return func(t *http.Transport) http.RoundTripper {
		var check func(*url.URL) error
		if identity != "" {
			check = func(u *url.URL) error {
				if p.IdentityHosts.Match(u) || also.Match(u) {
					return nil
				}
				return identityError(identity, u)
			}
		}
		return p.guard().Transport(t, check)
	}
}
