package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var baiduDirCandidates = []string{
	"/vol1/1000/baidu",
	"/vol1/1000/Baidu",
	"/vol1/1000/BaiduNetdiskDownload",
	"/vol1/1000/baidunetdiskdownload",
	"/vol1/1000/BaiduNetdisk",
	"/vol1/1000/百度网盘下载",
	"/vol1/1000/百度网盘",
	"/vol1/1000/downloads/BaiduNetdiskDownload",
	"/vol1/1000/Downloads/BaiduNetdiskDownload",
	"/vol1/1000/下载/百度网盘",
	"/vol1/baidu",
	"/vol1/BaiduNetdiskDownload",
	"/vol1/downloads/BaiduNetdiskDownload",
	"/vol1/Downloads/BaiduNetdiskDownload",
	"/vol1/百度网盘下载",
}

func existingBaiduDirs() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range baiduDirCandidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func partialFileName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	low := strings.ToLower(name)
	if partVolumeRE.MatchString(low) {
		return false
	}
	for _, marker := range []string{
		".baiduyun.", ".baiduyun", ".downloading", ".bdtmp", ".part",
		".crdownload", ".aria2", ".!qb", ".td", ".xltd", ".bc!", ".tmp",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// fileOpenForWrite reports whether any process still holds the file open for
// writing. It relies on /proc and is a no-op on non-Linux platforms.
func fileOpenForWrite(path string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	self := os.Getpid()
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == self {
			continue
		}
		fdDir := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || link != target {
				continue
			}
			info, err := os.ReadFile(filepath.Join("/proc", p.Name(), "fdinfo", fd.Name()))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(info), "\n") {
				if !strings.HasPrefix(line, "flags:") {
					continue
				}
				v := strings.TrimSpace(strings.TrimPrefix(line, "flags:"))
				n, err := strconv.ParseUint(v, 8, 64)
				if err == nil && n&3 != 0 {
					return true
				}
			}
		}
	}
	return false
}
