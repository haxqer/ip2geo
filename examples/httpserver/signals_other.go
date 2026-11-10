//go:build !unix

package main

import "os"

var (
	// stopSignals make the server shut down gracefully.
	stopSignals = []os.Signal{os.Interrupt}
	// reloadSignals is empty: without SIGHUP, only polling reloads.
	reloadSignals []os.Signal
)
