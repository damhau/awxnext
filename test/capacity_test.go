package test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/capacity"
)

// AWX's capacity formulas (awx.main.utils.common), with the values AWX
// gives for the same CPU, memory and settings.
func TestCapacityFormula(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	const gib = int64(1) << 30
	check := func(what string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %v, want %v", what, got, want)
		}
	}
	check("4 CPUs", capacity.CPU(4, false), 16)
	check("1.5 CPUs", capacity.CPU(1.5, false), 6)
	check("0.1 CPU", capacity.CPU(0.1, false), 1)
	check("8 GiB", capacity.Memory(8*gib, false), 61)
	check("16 GiB", capacity.Memory(16*gib, false), 143)
	check("1 GiB", capacity.Memory(gib, false), 1)
	check("between, 0", capacity.Adjusted(16, 61, 0), 16)
	check("between, 1", capacity.Adjusted(61, 16, 1), 61)
	check("between, 0.5", capacity.Adjusted(16, 61, 0.5), 38)
	check("between, 0.29", capacity.Adjusted(16, 61, 0.29), 29)

	t.Setenv("SYSTEM_TASK_FORKS_CPU", "2")
	t.Setenv("SYSTEM_TASK_FORKS_MEM", "200")
	check("2 forks per CPU", capacity.CPU(4, false), 8)
	check("200 MB per fork", capacity.Memory(8*gib, false), 30)
	t.Setenv("SYSTEM_TASK_FORKS_CPU", "")
	t.Setenv("SYSTEM_TASK_FORKS_MEM", "")

	// In Kubernetes no memory is set aside for the other services.
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	check("8 GiB in Kubernetes", capacity.Memory(8*gib, false), 81)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	// SYSTEM_TASK_ABS_* replace a control node's resources only.
	t.Setenv("SYSTEM_TASK_ABS_CPU", "1500m")
	t.Setenv("SYSTEM_TASK_ABS_MEM", "4Gi")
	check("control, absolute CPU", capacity.CPU(64, true), 6)
	check("execution, absolute CPU", capacity.CPU(64, false), 256)
	check("control, absolute memory", capacity.Memory(64*gib, true), 20)
	check("execution, absolute memory", capacity.Memory(64*gib, false), 634)

	for in, want := range map[string]float64{"2": 2, "250m": 0.2, "0.04": 0.1, "1.26": 1.3, "x": 1} {
		check("cpu "+in, capacity.ParseCPU(in), want)
	}
	for in, want := range map[string]int64{"1024": 1024, "500M": 500_000_000, "4Gi": 4 * gib, "2Ki": 2048, "4GB": 1, "Gi": 1} {
		check("memory "+in, capacity.ParseMemory(in), want)
	}
}

// The instances' capacities as AWX computes them: a control node from its
// own resources, corrected by SYSTEM_TASK_ABS_*; an execution node from
// what its agent reports; capacity_adjustment between the two.
func TestInstanceCapacity(t *testing.T) {
	a := adminClient(t)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	node := must(t, a.get("instances/?hostname=test-node"), 200).results()[0]
	id := int64(node["id"].(float64))
	path := fmt.Sprintf("instances/%d/", id)
	t.Cleanup(func() {
		a.patch(path, map[string]any{"capacity_adjustment": 1, "enabled": true})
		a.post(path+"health_check/", nil)
	})
	num := func(r resp, f string) int {
		t.Helper()
		return int(r.Body[f].(float64))
	}
	// AWX's formula, written out: 4 forks per CPU, 100 MB per fork after
	// 2 GiB, at least 1.
	cpuForks := func(cpu float64) int { return max(1, int(cpu*4)) }
	memForks := func(mem int64) int { return max(1, int(((mem-2<<30)>>20)/100)) }

	r := must(t, a.get(path), 200)
	if lo, hi := num(r, "cpu_capacity"), num(r, "mem_capacity"); lo != cpuForks(r.Body["cpu"].(float64)) || hi != memForks(int64(r.Body["memory"].(float64))) ||
		num(r, "capacity") != max(lo, hi) {
		t.Errorf("measured control node: %v", r.Body)
	}

	t.Setenv("SYSTEM_TASK_ABS_CPU", "2")
	t.Setenv("SYSTEM_TASK_ABS_MEM", "8Gi")
	must(t, a.post(path+"health_check/", nil), 200)
	r = must(t, a.get(path), 200)
	if r.Body["cpu"] != float64(2) || r.Body["memory"] != float64(8<<30) || num(r, "cpu_capacity") != 8 || num(r, "mem_capacity") != 61 || num(r, "capacity") != 61 {
		t.Errorf("corrected control node: %v", r.Body)
	}
	for _, c := range []struct {
		adj  float64
		want int
	}{{0, 8}, {0.5, 34}, {0.25, 21}, {1, 61}} {
		if got := num(must(t, a.patch(path, map[string]any{"capacity_adjustment": c.adj}), 200), "capacity"); got != c.want {
			t.Errorf("adjustment %v: capacity %d, want %d", c.adj, got, c.want)
		}
	}
	// Disabled instances have none.
	if r := must(t, a.patch(path, map[string]any{"enabled": false}), 200); num(r, "capacity") != 0 {
		t.Errorf("disabled: %v", r.Body["capacity"])
	}
	must(t, a.patch(path, map[string]any{"enabled": true}), 200)
	if g := must(t, a.get("instance_groups/?name=controlplane"), 200).results()[0]; g["capacity"].(float64) < 61 {
		t.Errorf("group capacity: %v", g["capacity"])
	}
	// AWX's DecimalField(max_digits=3, decimal_places=2), at least 0.
	for adj, msg := range map[float64]string{-0.5: "Ensure this value is greater than or equal to 0.", 0.123: "Ensure that there are no more than 2 decimal places.",
		12.34: "Ensure that there are no more than 3 digits in total.", 10.5: "Ensure that there are no more than 1 digits before the decimal point."} {
		if r := a.patch(path, map[string]any{"capacity_adjustment": adj}); r.Status != 400 || !strings.Contains(r.Raw, msg) {
			t.Errorf("adjustment %v: %d %s", adj, r.Status, r.Raw)
		}
	}

	// An execution node: what its agent reports, without the corrections.
	inst := must(t, a.post("instances/", map[string]any{"hostname": uniq("exec"), "node_type": "execution"}), 201).id()
	t.Cleanup(func() { a.delete(fmt.Sprintf("instances/%d/", inst)) })
	tok := must(t, a.post(fmt.Sprintf("instances/%d/agent_token/", inst), nil), 201).Body["token"].(string)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		agent.RunAgent(ctx, agent.Config{URL: srv.URL, Token: tok, Runtime: "none", WorkDir: t.TempDir(), Version: "test"})
		close(stopped)
	}()
	defer func() { cancel(); <-stopped }()
	e := waitInstance(t, a, inst, "ready")
	if lo, hi := num(e, "cpu_capacity"), num(e, "mem_capacity"); lo != cpuForks(e.Body["cpu"].(float64)) || hi != memForks(int64(e.Body["memory"].(float64))) ||
		num(e, "capacity") != max(lo, hi) || e.Body["cpu"] == float64(2) {
		t.Errorf("execution node: %v", e.Body)
	}
}
