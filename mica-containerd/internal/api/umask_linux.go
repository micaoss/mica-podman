package api

import "syscall"

// umask sets the process umask and returns the old one, so the socket is created 0600.
func umask(mask int) int { return syscall.Umask(mask) }
