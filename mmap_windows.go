package ip2geo

import (
	"os"
	"syscall"
	"unsafe"
)

// mmapFile maps the first size bytes of f read-only. The mapping outlives f.
func mmapFile(f *os.File, size int) ([]byte, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var addr uintptr
	var merr error
	if err := rc.Control(func(fd uintptr) {
		s := uint64(size)
		h, err := syscall.CreateFileMapping(syscall.Handle(fd), nil, syscall.PAGE_READONLY, uint32(s>>32), uint32(s), nil)
		if err != nil {
			merr = os.NewSyscallError("CreateFileMapping", err)
			return
		}
		// The view keeps the mapping object alive after its handle is closed.
		addr, err = syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
		if err != nil {
			merr = os.NewSyscallError("MapViewOfFile", err)
		}
		_ = syscall.CloseHandle(h)
	}); err != nil {
		return nil, err
	}
	if merr != nil {
		return nil, merr
	}
	// Convert through a pointer to the address: the view is not Go memory,
	// and a direct uintptr conversion is what go vet's unsafeptr check flags.
	return unsafe.Slice(*(**byte)(unsafe.Pointer(&addr)), size), nil
}

func munmap(m []byte) error {
	return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(unsafe.SliceData(m))))
}
