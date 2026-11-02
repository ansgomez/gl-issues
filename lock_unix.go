//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// lockDir takes an exclusive flock on dir, or fails with errLocked when
// another process holds it; unlock releases it. The lock is advisory:
// only gl-issues takes it, an editor saving a file in dir does not.
func lockDir(dir string) (unlock func(), err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil // closing the directory releases the lock
}
