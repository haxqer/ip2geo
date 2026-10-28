//go:build !unix && !windows

package ip2geo

import (
	"errors"
	"os"
)

var errNoMmap = errors.New("memory mapping is not supported on this platform")

// mmapFile always fails here, so Open reads the file into the heap.
func mmapFile(*os.File, int) ([]byte, error) { return nil, errNoMmap }

func munmap([]byte) error { return errNoMmap }
