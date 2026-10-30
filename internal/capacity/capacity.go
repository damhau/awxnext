// Package capacity is AWX's instance capacity: forks from CPU and memory
// (awx.main.utils.common), and capacity_adjustment choosing between the
// two (Instance.set_capacity_value).
//
// AWX's settings are read from the environment, where AWX looks first:
// SYSTEM_TASK_FORKS_CPU (forks per CPU, 4), SYSTEM_TASK_FORKS_MEM (MB of
// memory per fork, 100), and SYSTEM_TASK_ABS_CPU and SYSTEM_TASK_ABS_MEM,
// which replace a control node's measured CPU and memory.
package capacity

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// IsK8S is AWX's IS_K8S: in a pod, the web server and the database have
// memory of their own, so no memory is set aside for them.
func IsK8S() bool { return os.Getenv("KUBERNETES_SERVICE_HOST") != "" }

// CorrectedCPU is get_corrected_cpu: SYSTEM_TASK_ABS_CPU, when set,
// replaces the CPU count.
func CorrectedCPU(cpu float64) float64 {
	if s, ok := os.LookupEnv("SYSTEM_TASK_ABS_CPU"); ok {
		return ParseCPU(s)
	}
	return cpu
}

// CorrectedMemory is get_corrected_memory, for SYSTEM_TASK_ABS_MEM.
func CorrectedMemory(mem int64) int64 {
	if s, ok := os.LookupEnv("SYSTEM_TASK_ABS_MEM"); ok {
		return ParseMemory(s)
	}
	return mem
}

// CPU is get_cpu_effective_capacity: forks for a CPU count.
func CPU(cpu float64, control bool) int {
	if control {
		cpu = CorrectedCPU(cpu)
	}
	return max(1, int(cpu*float64(setting("SYSTEM_TASK_FORKS_CPU", 4))))
}

// Memory is get_mem_effective_capacity: forks for memory in bytes, after
// 2 GiB for the other services outside Kubernetes.
func Memory(mem int64, control bool) int {
	if control {
		mem = CorrectedMemory(mem)
	}
	perFork := int64(setting("SYSTEM_TASK_FORKS_MEM", 100))
	if perFork <= 0 {
		perFork = 100
	}
	var penalty int64 = 2 << 30
	if IsK8S() {
		penalty = 0
	}
	return int(max(1, floorDiv(floorDiv(mem-penalty, 1<<20), perFork)))
}

// Adjusted is set_capacity_value for an enabled instance: the smaller of
// the CPU and memory capacities at adjustment 0, the larger at 1, and in
// between proportionally, AWX's adjustment having two decimals.
func Adjusted(cpuCap, memCap int, adjustment float64) int {
	lo, hi := int64(min(cpuCap, memCap)), int64(max(cpuCap, memCap))
	return int(lo + (hi-lo)*int64(math.Round(adjustment*100))/100)
}

// SQL is set_capacity_value in SQL, for the capacity column of a statement
// on instances, from the CPU and memory capacities given as SQL
// expressions: 0 for disabled instances and hop nodes.
func SQL(cpuCap, memCap string) string {
	return fmt.Sprintf(`CASE WHEN instances.enabled AND instances.node_type <> 'hop'
		THEN trunc(least(%[1]s, %[2]s) + (greatest(%[1]s, %[2]s) - least(%[1]s, %[2]s)) * instances.capacity_adjustment)::int ELSE 0 END`,
		"("+cpuCap+")::int", "("+memCap+")::int")
}

// ParseCPU is convert_cpu_str_to_decimal_cpu: a number of CPUs, or
// millicores ("1500m"), at least 0.1; 1 when it cannot be read.
func ParseCPU(s string) float64 {
	milli := strings.HasSuffix(s, "m")
	cpu, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
	if err != nil {
		cpu, milli = 1, false
	}
	if milli {
		cpu /= 1000
	}
	return max(0.1, math.RoundToEven(cpu*10)/10)
}

var units = map[string]int64{
	"Ei": 1 << 60, "E": 1e18, "Pi": 1 << 50, "P": 1e15, "Ti": 1 << 40, "T": 1e12,
	"Gi": 1 << 30, "G": 1e9, "Mi": 1 << 20, "M": 1e6, "Ki": 1 << 10, "K": 1e3,
}

// ParseMemory is convert_mem_str_to_bytes: bytes, or a number with a
// Kubernetes suffix ("4Gi", "500M"); 1 byte when it cannot be read.
func ParseMemory(s string) int64 {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "+") {
		return n
	}
	i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if i <= 0 {
		return 1
	}
	n, _ := strconv.ParseInt(s[:i], 10, 64)
	unit, ok := units[s[i:]]
	if !ok {
		return 1
	}
	return max(1, n*unit)
}

// setting reads an integer setting as AWX does: unset or empty means the
// default.
func setting(name string, def int) int {
	if s := os.Getenv(name); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
