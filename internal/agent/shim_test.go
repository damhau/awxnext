package agent

import (
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/runnercmd"
)

// The container group shim (and remote agents) run the worker with the
// keepalive interval the control node sends in the work spec.
func TestShimRunsWorkerWithKeepalive(t *testing.T) {
	rs := runnerSpec(Config{Runtime: "none", RunnerCommand: "ansible-runner"}, WorkSpec{Ident: "7", KeepaliveSeconds: 15})
	rs.WorkDir = "/runner"
	args, _, err := runnercmd.Command(rs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--keepalive-seconds=15") {
		t.Fatalf("worker command: %v", args)
	}
}

// AWX_MOUNT_ISOLATED_PATHS_ON_K8S: show paths become hostPath volumes.
func TestPodMountsShowPaths(t *testing.T) {
	pod, _, err := BuildPod(PodRequest{Spec: WorkSpec{Image: "ee"}, MountPaths: []string{"/srv/data", "/etc/pki/ca:/certs:ro"}}, "https://x/unit", "tok")
	if err != nil {
		t.Fatal(err)
	}
	spec := pod["spec"].(map[string]any)
	var hostPaths []string
	for _, v := range spec["volumes"].([]any) {
		if hp, ok := v.(map[string]any)["hostPath"].(map[string]any); ok {
			hostPaths = append(hostPaths, hp["path"].(string))
		}
	}
	if strings.Join(hostPaths, ",") != "/srv/data,/etc/pki/ca" {
		t.Fatalf("hostPath volumes: %v", hostPaths)
	}
	var certs map[string]any
	for _, m := range spec["containers"].([]any)[0].(map[string]any)["volumeMounts"].([]any) {
		if m.(map[string]any)["mountPath"] == "/certs" {
			certs = m.(map[string]any)
		}
	}
	if certs == nil || certs["readOnly"] != true {
		t.Fatalf("mount of /certs: %v", certs)
	}
}
