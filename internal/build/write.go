package build

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/haxqer/ip2geo"
)

// MaxFileSize is the size limit of a database file: ip2geo.Open and
// ip2geo.OpenBytes refuse larger inputs with ip2geo.ErrTooLarge.
const MaxFileSize = 1 << 31

// maxFileSize is MaxFileSize, lowered by tests.
var maxFileSize uint64 = MaxFileSize

// CheckDatabase checks that buf, a database the builder encoded, is one
// ip2geo.OpenBytes accepts: not over MaxFileSize, and valid by every rule a
// reader checks, the section checksums included. An error here is a bug in
// the builder, or a source it does not handle; the size error wraps
// ip2geo.ErrTooLarge.
func CheckDatabase(buf []byte) error {
	if uint64(len(buf)) > maxFileSize {
		return fmt.Errorf("built database is %d bytes, over the %d-byte limit of the format: %w",
			len(buf), maxFileSize, ip2geo.ErrTooLarge)
	}
	if _, err := ip2geo.OpenBytes(buf, ip2geo.NoCopy()); err != nil {
		return fmt.Errorf("built database does not load: %w", err)
	}
	return nil
}

// WriteDatabase checks buf with CheckDatabase and, only if it passes,
// writes it to path with WriteFileAtomic. On any error an existing path is
// left untouched.
func WriteDatabase(path string, buf []byte) error {
	if err := CheckDatabase(buf); err != nil {
		return fmt.Errorf("%w; %q not written", err, path)
	}
	return WriteFileAtomic(path, buf)
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory that is renamed over path only after write, sync and close
// succeed; then, where the system supports it, the directory is synced too,
// so the rename survives a crash. On any error the temporary file is
// removed and an existing path is left untouched. A new file gets mode
// 0644; a replaced file keeps its permission bits. If path is a symbolic
// link, the link itself is replaced.
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
	syncDir(dir)
	return nil
}
