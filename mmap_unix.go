//go:build unix

package ip2geo

import (
	"os"
	"syscall"
)

// mmapFile maps the first size bytes of f read-only. The mapping outlives f.
func mmapFile(f *os.File, size int) ([]byte, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var m []byte
	var merr error
	if err := rc.Control(func(fd uintptr) {
		m, merr = syscall.Mmap(int(fd), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	}); err != nil {
		return nil, err
	}
	return m, merr
}

func munmap(m []byte) error { return syscall.Munmap(m) }
