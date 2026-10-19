package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ArchiveEntry struct {
	Name      string
	Size      int64
	CRC       uint32
	Encrypted bool
	IsDir     bool
}

type ArchiveInfo struct {
	Kind            string
	Encrypted       bool
	HeaderEncrypted bool
	Comment         string
	Entries         []ArchiveEntry
	CommonRoot      string
	TestEntry       string
	TestSize        int64
	TestOffset      int64
	TestCRC         uint32
}

var magicKinds = []struct {
	kind string
	off  int
	sig  []byte
}{
	{"7z", 0, []byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c}},
	{"rar", 0, []byte{0x52, 0x61, 0x72, 0x21, 0x1a, 0x07}},
	{"zip", 0, []byte{0x50, 0x4b, 0x03, 0x04}},
	{"zip", 0, []byte{0x50, 0x4b, 0x05, 0x06}},
	{"zip", 0, []byte{0x50, 0x4b, 0x07, 0x08}},
	{"gzip", 0, []byte{0x1f, 0x8b}},
	{"bzip2", 0, []byte{0x42, 0x5a, 0x68}},
	{"xz", 0, []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}},
	{"tar", 257, []byte("ustar")},
}

var skipExt = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".ts": true, ".m2ts": true, ".flv": true,
	".wmv": true, ".mov": true, ".rmvb": true, ".webm": true, ".mpg": true, ".mpeg": true,
	".mp3": true, ".flac": true, ".wav": true, ".aac": true, ".m4a": true, ".ape": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true,
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".nfo": true,
	".exe": true, ".msi": true, ".apk": true, ".ipa": true, ".dmg": true, ".pkg": true,
	".iso": true, ".img": true, ".vhd": true, ".vhdx": true, ".vmdk": true,
	".docx": true, ".xlsx": true, ".pptx": true, ".doc": true, ".xls": true, ".ppt": true,
	".epub": true, ".jar": true, ".war": true, ".whl": true, ".nupkg": true, ".vsix": true,
	".crx": true, ".xpi": true, ".odt": true, ".ods": true, ".odp": true,
	".cbz": true, ".cbr": true, ".pdf": true, ".torrent": true,
	".tmp": true, ".part": true, ".crdownload": true, ".downloading": true,
	".aria2": true, ".!qb": true, ".td": true, ".xltd": true, ".bc!": true,
}

var secondaryVolumeRE = regexp.MustCompile(`(?i)(?:\.part(\d+)\.rar|\.(7z|zip|rar)\.(\d{3})|\.(\d{3})|\.[rsz](\d{2}))$`)

func detectArchive(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if skipExt[ext] {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	head = head[:n]
	for _, m := range magicKinds {
		if m.off+len(m.sig) > len(head) {
			continue
		}
		if bytes.Equal(head[m.off:m.off+len(m.sig)], m.sig) {
			return m.kind, nil
		}
	}
	return "", nil
}

func isSecondaryVolume(name string) bool {
	m := secondaryVolumeRE.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return false
	}
	if m[1] != "" {
		n, _ := strconv.Atoi(m[1])
		return n != 1
	}
	if m[3] != "" {
		return m[3] != "001"
	}
	if m[4] != "" {
		return m[4] != "001"
	}
	if m[5] != "" {
		return true
	}
	return true
}

func archiveBaseName(path string) string {
	base := secondaryVolumeRE.ReplaceAllString(filepath.Base(path), "")
	for {
		changed := false
		low := strings.ToLower(base)
		for _, ext := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tbz2", ".txz", ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz"} {
			if strings.HasSuffix(low, ext) {
				base = base[:len(base)-len(ext)]
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	base = strings.TrimSpace(base)
	if base == "" || base == "." {
		base = "archive"
	}
	return base
}

func targetBaseName(path string) string {
	base := archiveBaseName(path)
	if base == filepath.Base(path) {
		if ext := filepath.Ext(base); ext != "" && len(ext) <= 12 {
			base = strings.TrimSuffix(base, ext)
		}
	}
	base = strings.TrimSpace(base)
	if base == "" || base == "." {
		base = "archive"
	}
	return base
}

func run7z(bin string, timeout time.Duration, args ...string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "LANGUAGE=C")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errOut.String(), -1, fmt.Errorf("7-Zip 超时")
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return out.String(), errOut.String(), -1, err
		}
	}
	return out.String(), errOut.String(), code, nil
}

func inspectArchive(bin, path string) (*ArchiveInfo, error) {
	info := &ArchiveInfo{}
	kind, err := detectArchive(path)
	if err != nil {
		return nil, err
	}
	info.Kind = kind
	out, errOut, code, err := run7z(bin, 60*time.Second, "l", "-slt", "-bd", "--", path)
	if err != nil {
		return nil, err
	}
	combined := out + "\n" + errOut
	if code != 0 {
		if first, _ := isFirstVolumePath(path); first && isIncompleteVolumeError(combined) {
			return nil, fmt.Errorf("%w: %s", errIncompleteVolumes, tailText(combined, 300))
		}
		if looksEncrypted(combined) {
			info.Encrypted = true
			info.HeaderEncrypted = true
			return info, nil
		}
		return nil, fmt.Errorf("无法读取压缩包: %s", tailText(combined, 500))
	}

	started := false
	var cur *ArchiveEntry
	flush := func() {
		if cur != nil {
			cur.IsDir = cur.IsDir || strings.HasSuffix(cur.Name, "/")
			cur.Name = strings.TrimSuffix(cur.Name, "/")
			info.Entries = append(info.Entries, *cur)
			cur = nil
		}
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "----------" {
			started = true
			continue
		}
		if !started {
			if strings.HasPrefix(line, "Comment = ") {
				info.Comment = strings.TrimPrefix(line, "Comment = ")
			}
			continue
		}
		if strings.HasPrefix(line, "Path = ") {
			flush()
			cur = &ArchiveEntry{Name: strings.TrimPrefix(line, "Path = ")}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Size = "):
			cur.Size, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Size = ")), 10, 64)
		case strings.HasPrefix(line, "Encrypted = "):
			cur.Encrypted = strings.TrimSpace(strings.TrimPrefix(line, "Encrypted = ")) == "+"
		case strings.HasPrefix(line, "CRC = "):
			v := strings.TrimSpace(strings.TrimPrefix(line, "CRC = "))
			if v != "" {
				if n, err := strconv.ParseUint(v, 16, 32); err == nil {
					cur.CRC = uint32(n)
				}
			}
		case strings.HasPrefix(line, "Folder = "):
			cur.IsDir = strings.TrimSpace(strings.TrimPrefix(line, "Folder = ")) == "+"
		}
	}
	flush()

	var offset int64
	var best *ArchiveEntry
	var bestOffset int64
	for i := range info.Entries {
		e := &info.Entries[i]
		if e.IsDir {
			continue
		}
		if e.Encrypted {
			info.Encrypted = true
			if e.Size > 0 && (best == nil || best.Size <= 0 || e.Size < best.Size) {
				best = e
				bestOffset = offset
			}
		}
		offset += e.Size
	}
	if best != nil {
		info.TestEntry = best.Name
		info.TestSize = best.Size
		info.TestOffset = bestOffset
		info.TestCRC = best.CRC
	}
	info.CommonRoot = commonRoot(info.Entries)
	return info, nil
}

func looksEncrypted(s string) bool {
	low := strings.ToLower(s)
	for _, marker := range []string{
		"wrong password",
		"encrypted archive",
		"cannot open encrypted",
		"enter password",
		"password is incorrect",
		"data error in encrypted",
		"encrypted file",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

func commonRoot(entries []ArchiveEntry) string {
	root := ""
	seenAny := false
	for _, e := range entries {
		p := strings.ReplaceAll(e.Name, "\\", "/")
		p = strings.TrimPrefix(p, "./")
		trimmed := strings.Trim(p, "/")
		if trimmed == "" {
			continue
		}
		seenAny = true
		parts := strings.Split(trimmed, "/")
		if len(parts) == 1 {
			if e.IsDir {
				if root == "" {
					root = parts[0]
				} else if root != parts[0] {
					return ""
				}
				continue
			}
			return ""
		}
		if root == "" {
			root = parts[0]
		} else if root != parts[0] {
			return ""
		}
	}
	if !seenAny {
		return ""
	}
	return root
}

func unsafeEntry(name string) bool {
	p := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return true
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return true
		}
	}
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	return strings.ContainsRune(p, 0)
}

func testPassword(bin, path string, info *ArchiveInfo, pass string) (bool, string) {
	if info.HeaderEncrypted {
		out, errOut, code, err := run7z(bin, 60*time.Second, "l", "-slt", "-bd", "-p"+pass, "--", path)
		if err != nil {
			return false, err.Error()
		}
		if code == 0 && !looksEncrypted(out+errOut) {
			return true, ""
		}
		return false, tailText(out+"\n"+errOut, 200)
	}
	if info.TestEntry != "" {
		return testPasswordStream(bin, path, pass, info.TestOffset, info.TestSize, info.TestCRC)
	}
	return true, ""
}

func testPasswordStream(bin, path, pass string, offset, size int64, crc uint32) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "x", "-so", "-bd", "-p"+pass, "--", path)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "LANGUAGE=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err.Error()
	}
	var errBuf limitedBuffer
	errBuf.limit = 16 * 1024
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return false, err.Error()
	}

	if offset > 0 {
		if _, err := io.CopyN(io.Discard, stdout, offset); err != nil {
			_ = cmd.Wait()
			return false, tailText(errBuf.String(), 200)
		}
	}

	const maxVerify = int64(64 << 20)
	readN := size
	if readN <= 0 || readN > maxVerify {
		readN = 1
	}
	var writer io.Writer = io.Discard
	var hash = crc32.NewIEEE()
	if crc != 0 && size <= maxVerify {
		writer = hash
	}
	n, err := io.CopyN(writer, stdout, readN)
	if err != nil || n < readN {
		_ = cmd.Wait()
		return false, tailText(errBuf.String(), 200)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if writer == hash && hash.Sum32() != crc {
		return false, "CRC 校验失败"
	}
	return true, ""
}

func extractArchive(bin, path, target, pass string, onProgress func(int)) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	args := []string{"x", "-y", "-bd", "-bso0", "-bsp1", "-o" + target}
	if pass != "" {
		args = append(args, "-p"+pass)
	}
	args = append(args, "--", path)

	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "LANGUAGE=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf limitedBuffer
	errBuf.limit = 64 * 1024
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return err
	}

	progressRE := regexp.MustCompile(`(\d{1,3})%`)
	reader := bufio.NewReaderSize(stdout, 8192)
	buf := make([]byte, 4096)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if m := progressRE.FindAllSubmatch(buf[:n], -1); len(m) > 0 {
				last := m[len(m)-1]
				if v, err := strconv.Atoi(string(last[1])); err == nil && onProgress != nil {
					onProgress(v)
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("解压失败: %s", tailText(errBuf.String(), 700))
	}
	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if l.buf.Len() < l.limit {
		remain := l.limit - l.buf.Len()
		if len(p) > remain {
			p = p[:remain]
		}
		l.buf.Write(p)
	}
	return n, nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }

func tailText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

func sevenZipCandidates() []string {
	name := "7zz"
	if isWindows() {
		name = "7z.exe"
	}
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	out := []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "7zz", name),
		filepath.Join(dir, "7zz-"+archName()),
		filepath.Join(dir, "7zz", "7zz-"+archName()),
	}
	if p, err := exec.LookPath("7zz"); err == nil {
		out = append(out, p)
	}
	if p, err := exec.LookPath("7z"); err == nil {
		out = append(out, p)
	}
	if p, err := exec.LookPath("7za"); err == nil {
		out = append(out, p)
	}
	return out
}

func find7zz(override string) string {
	if override != "" {
		if fileExists(override) {
			return override
		}
	}
	for _, p := range sevenZipCandidates() {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}

func archName() string { return runtime.GOARCH }
