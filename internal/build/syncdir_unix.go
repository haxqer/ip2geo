//go:build unix

package build

import "os"

// syncDir syncs directory dir, so that a rename into it is durable. It is
// best effort: the rename has already happened, and some file systems
// refuse to sync a directory, so errors are ignored.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
