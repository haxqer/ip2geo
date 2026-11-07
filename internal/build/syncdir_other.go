//go:build !unix

package build

// syncDir does nothing: outside Unix (on Windows, for example) there is no
// portable way to sync a directory, so the rename is as durable as the file
// system makes it.
func syncDir(string) {}
