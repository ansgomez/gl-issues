//go:build !unix

package main

// lockDir is a no-op where flock is not available.
func lockDir(string) (unlock func(), err error) { return func() {}, nil }
