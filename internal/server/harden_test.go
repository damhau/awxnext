//go:build linux

package server

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A job on the "none" runtime is a child process of the server's user. Once
// the server is hardened it must not read the server's environment, where
// the secret key and the database URL live.
func TestHardenHidesEnvironmentFromJobs(t *testing.T) {
	read := func(harden bool) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHardenHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "AWXNEXT_HARDEN_HELPER=1", "AWXNEXT_TEST_SECRET=s3cr3t-marker", fmt.Sprintf("AWXNEXT_HARDEN=%v", harden))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, out)
		}
		return string(out)
	}
	if !strings.Contains(read(false), "READABLE") {
		t.Skip("this system already hides /proc/<pid>/environ from processes of the same user")
	}
	if out := read(true); !strings.Contains(out, "HIDDEN") {
		t.Fatalf("a child process still reads the hardened server's environment:\n%s", out)
	}
}

// TestHardenHelper is the server in TestHardenHidesEnvironmentFromJobs.
func TestHardenHelper(t *testing.T) {
	if os.Getenv("AWXNEXT_HARDEN_HELPER") != "1" {
		t.Skip("helper process of TestHardenHidesEnvironmentFromJobs")
	}
	if os.Getenv("AWXNEXT_HARDEN") == "true" {
		if err := Harden(); err != nil {
			t.Fatal(err)
		}
	}
	// What a task running on localhost could do.
	env, _ := exec.Command("cat", fmt.Sprintf("/proc/%d/environ", os.Getpid())).Output()
	if bytes.Contains(env, []byte("s3cr3t-marker")) {
		fmt.Println("READABLE")
	} else {
		fmt.Println("HIDDEN")
	}
}
