// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package doctor

import (
	"os"
	"strconv"
	"syscall"
)

func fileGID(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Gid), 10)
	}
	return ""
}

func freeBytes(p string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // block size is positive
}
