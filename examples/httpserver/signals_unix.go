//go:build unix

package main

import (
	"os"
	"syscall"
)

var (
	// stopSignals make the server shut down gracefully.
	stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	// reloadSignals make it reopen the database file at once.
	reloadSignals = []os.Signal{syscall.SIGHUP}
)
