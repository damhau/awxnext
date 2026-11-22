package runnercmd

import (
	"context"
	"os"
	"path/filepath"
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

func TestImageDigest(t *testing.T) {
	const a, b = "4a1f0000000000000000000000000000000000000000000000000000000000aa", "9c0e0000000000000000000000000000000000000000000000000000000000bb"
	bin := t.TempDir()
	// The fake runtime prints what `image inspect --format` would, by image.
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = 'image inspect --format' ] && [ \"$5\" = -- ] || exit 2\ncase \"$6\" in\n" +
		"quay.io/ansible/awx-ee:latest) echo 'mirror.example/awx-ee@sha256:" + b + " quay.io/ansible/awx-ee@sha256:" + a + " |" + b + "' ;;\n" +
		"built:1) echo '|" + b + "' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Runtime: "podman", Image: "quay.io/ansible/awx-ee:latest"}, "sha256:" + a},
		{Spec{Runtime: "podman", Image: "built:1"}, "sha256:" + b},
		{Spec{Runtime: "podman", Image: "missing:1"}, ""},
		{Spec{Runtime: "podman", Image: "quay.io/ansible/awx-ee@sha256:" + b}, "sha256:" + b},
		{Spec{Runtime: "none", Image: "quay.io/ansible/awx-ee:latest"}, ""},
	} {
		if got := ImageDigest(ctx, c.spec); got != c.want {
			t.Errorf("%s: %q, want %q", c.spec.Image, got, c.want)
		}
	}
	for in, want := range map[string]string{
		"docker-pullable://quay.io/ansible/awx-ee@sha256:" + a: "sha256:" + a,
		"sha256:" + b: "sha256:" + b,
		"":            "",
		"sha256:abc":  "",
	} {
		if got := Digest(in); got != want {
			t.Errorf("Digest(%q) = %q, want %q", in, got, want)
		}
	}
}
