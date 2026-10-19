package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var errIncompleteVolumes = errors.New("分卷不完整")

var (
	numericVolumeRE = regexp.MustCompile(`(?i)^(.*)\.(\d{3})$`)
	partVolumeRE    = regexp.MustCompile(`(?i)^(.*)\.part(\d+)\.rar$`)
	rarPartRE       = regexp.MustCompile(`(?i)^(.*)\.r(\d{2})$`)
	zipPartRE       = regexp.MustCompile(`(?i)^(.*)\.z(\d{2})$`)
)

// isFirstVolumePath reports whether path is the first file of a multi-volume
// archive and returns the family prefix used to find sibling volumes.
func isFirstVolumePath(path string) (bool, string) {
	name := filepath.Base(path)
	dir := filepath.Dir(path)

	if m := partVolumeRE.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n == 1, m[1]
	}
	if m := numericVolumeRE.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n == 1, m[1]
	}

	low := strings.ToLower(name)
	if strings.HasSuffix(low, ".rar") {
		base := name[:len(name)-4]
		if fileExists(filepath.Join(dir, base+".r00")) {
			return true, base
		}
	}
	if strings.HasSuffix(low, ".zip") {
		base := name[:len(name)-4]
		if fileExists(filepath.Join(dir, base+".z01")) {
			return true, base
		}
	}
	return false, ""
}

func isVolumePartName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	if rest == ".rar" || rest == ".zip" {
		return true
	}
	if numericVolumeRE.MatchString(name) {
		return true
	}
	if partVolumeRE.MatchString(name) {
		return true
	}
	if rarPartRE.MatchString(name) || zipPartRE.MatchString(name) {
		return true
	}
	return false
}

func volumePartPaths(path string) []string {
	first, prefix := isFirstVolumePath(path)
	if !first {
		return nil
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if partialFileName(name) {
			continue
		}
		if isVolumePartName(name, prefix) {
			parts = append(parts, filepath.Join(dir, name))
		}
	}
	sort.Strings(parts)
	return parts
}

func volumeSignature(path string) string {
	parts := volumePartPaths(path)
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s|%d|%d;", filepath.Base(p), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

func isIncompleteVolumeError(msg string) bool {
	low := strings.ToLower(msg)
	for _, marker := range []string{
		"unexpected end of archive",
		"missing volume",
		"cannot find volume",
		"cannot open the file as [7z] archive",
		"cannot open the file as [zip] archive",
		"cannot open the file as [rar] archive",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}
