//go:build !linux

package server

// Harden does nothing outside Linux.
func Harden() error { return nil }
