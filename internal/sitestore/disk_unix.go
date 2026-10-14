//go:build linux || darwin || freebsd || netbsd || openbsd

package sitestore

import "syscall"

// diskFree is the number of bytes a process may still write on the volume that holds dir.
func diskFree(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
