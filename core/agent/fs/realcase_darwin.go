package agentfs

import (
	"os"
	"syscall"
	"unsafe"
)

// fGetPath is F_GETPATH (sys/fcntl.h) returns the path as stored on disk.
const fGetPath = 50

// onDiskCase returns p with the case the (case-insensitive) volume stores, like realpath(3),
// which Rust's fs::canonicalize uses; p is returned unchanged if it can't be asked.
func onDiskCase(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return p
	}
	defer f.Close()
	var buf [1024]byte
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), fGetPath, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return p
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return p
}
