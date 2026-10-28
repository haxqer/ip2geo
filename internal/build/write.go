package build

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path through a temporary file in the same
// directory that is renamed over path only after write, sync and close
// succeed. On any error the temporary file is removed and an existing path
// is left untouched. A new file gets mode 0644; a replaced file keeps its
// permission bits. If path is a symbolic link, the link itself is replaced.
func WriteFileAtomic(path string, data []byte) (err error) {
	perm := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".geoc-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %q: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close() // already closed on the rename path; the error is moot
			os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("write %q: %w", tmp, err)
	}
	if err = f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %q: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
