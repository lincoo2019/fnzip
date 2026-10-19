//go:build linux

package main

import (
	"os"
	"syscall"
)

func fileCreationTime(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Sec
	}
	return fi.ModTime().Unix()
}
