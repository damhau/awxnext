//go:build linux

package server

import "golang.org/x/sys/unix"

// Harden keeps the server's environment (secret key, database URL, key
// provider tokens) and memory out of reach of other processes of its user,
// such as jobs on the "none" runtime: a process that is not dumpable can only
// be inspected with CAP_SYS_PTRACE.
func Harden() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
