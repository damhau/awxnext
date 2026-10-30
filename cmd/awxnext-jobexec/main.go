// Command awxnext-jobexec runs a job's worker as a job user (see
// internal/jobexec): installed with the file capabilities setuid, setgid,
// chown and kill, executable by the server's group only.
//
//	awxnext-jobexec run USER DIR COMMAND [ARG...]
package main

import (
	"fmt"
	"os"

	"github.com/damhau/awxnext/internal/jobexec"
)

func main() {
	if len(os.Args) < 5 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: awxnext-jobexec run USER DIR COMMAND [ARG...]")
		os.Exit(2)
	}
	code, err := jobexec.Run("/etc/passwd", os.Args[2], os.Args[3], os.Args[4:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "awxnext-jobexec:", err)
		os.Exit(125)
	}
	os.Exit(code)
}
