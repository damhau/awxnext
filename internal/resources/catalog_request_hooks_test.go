package resources

import (
	"slices"
	"testing"
)

func TestRequestEvents(t *testing.T) {
	for _, c := range []struct {
		kind, itemKind, status, previous string
		want                             []string
	}{
		{"new", itemDeployment, requestPending, "", nil},
		{"new", itemDeployment, requestAwaiting, "", []string{"waiting"}},
		{"new", itemDeployment, requestPending, requestAwaiting, []string{"approved"}},
		{"new", itemDeployment, requestFailed, requestAwaiting, []string{"approved", "failed"}},
		{"new", itemRunOnce, requestSuccessful, requestAwaiting, []string{"approved", "done"}},
		{"new", itemDeployment, requestDenied, requestAwaiting, []string{"denied"}},
		{"extend", itemDeployment, requestCanceled, requestAwaiting, []string{"canceled"}},
		{"new", itemDeployment, requestRunning, requestPending, []string{"running"}},
		{"new", itemDeployment, requestSuccessful, requestRunning, []string{"ready"}},
		{"new", itemRunOnce, requestSuccessful, requestRunning, []string{"done"}},
		{"extend", itemDeployment, requestSuccessful, "", []string{"done"}},
		{"restart", itemDeployment, requestSuccessful, requestRunning, []string{"done"}},
		{"delete", itemDeployment, requestSuccessful, requestRunning, []string{"deleted"}},
		{"delete", itemDeployment, requestFailed, requestRunning, []string{"failed"}},
		{"new", itemDeployment, requestCanceled, requestRunning, []string{"canceled"}},
	} {
		if got := requestEvents(c.kind, c.itemKind, c.status, c.previous); !slices.Equal(got, c.want) {
			t.Errorf("%s %s, %q after %q: %v, want %v", c.kind, c.itemKind, c.status, c.previous, got, c.want)
		}
	}
}
