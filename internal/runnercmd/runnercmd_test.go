package runnercmd

import (
	"slices"
	"strings"
	"testing"
)

func TestCommandKeepaliveAndShowPaths(t *testing.T) {
	s := Spec{Runtime: "podman", Image: "ee:1", Name: "job_1", KeepaliveSeconds: 20,
		ShowPaths: []string{"/srv/data", "/etc/pki/ca:/certs:ro,z"}}
	args, cleanup, err := Command(s)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	line := strings.Join(args, " ")
	for _, want := range []string{"-v /srv/data:/srv/data", "-v /etc/pki/ca:/certs:ro,z", "ee:1 ansible-runner worker --private-data-dir=/runner --keepalive-seconds=20"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %s", want, line)
		}
	}

	// The none runtime sees the host already: no mounts, but keepalives.
	args, _, _ = Command(Spec{Runtime: "none", WorkDir: "/tmp/w", KeepaliveSeconds: 5, ShowPaths: []string{"/srv/data"}})
	if !slices.Equal(args, []string{"ansible-runner", "worker", "--private-data-dir=/tmp/w", "--keepalive-seconds=5"}) {
		t.Errorf("none: %v", args)
	}
	args, _, _ = Command(Spec{Runtime: "none", WorkDir: "/tmp/w"})
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--keepalive") }) {
		t.Errorf("keepalive without the setting: %v", args)
	}

	if _, _, err := Command(Spec{Runtime: "docker", Image: "ee", ShowPaths: []string{"relative/dir"}}); err == nil {
		t.Error("a relative show path was accepted")
	}
}
