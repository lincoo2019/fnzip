package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type observation struct {
	size    int64
	modTime int64
	since   time.Time
}

type Engine struct {
	state *State
	queue chan string
	stop  chan struct{}

	mu          sync.Mutex
	inFlight    map[string]bool
	observed    map[string]observation
	scanMu      sync.Mutex
	scanning    bool
	lastScan    time.Time
	foundLast   int
	skippedOpen int
	bin         string
	startedAt   time.Time
}

func NewEngine(state *State) *Engine {
	return &Engine{
		state:     state,
		queue:     make(chan string, 256),
		stop:      make(chan struct{}),
		inFlight:  map[string]bool{},
		observed:  map[string]observation{},
		startedAt: time.Now(),
	}
}

func (e *Engine) Start() {
	settings := e.state.GetSettings()
	if settings.SevenZip == "" || !fileExists(settings.SevenZip) {
		e.bin = find7zz("")
		e.state.SetSevenZip(e.bin)
	} else {
		e.bin = settings.SevenZip
	}
	go e.worker()
	go e.scanLoop()
	for _, id := range e.state.ResetInterrupted() {
		e.enqueueTask(id)
	}
	if settings.AutoScan {
		go e.scan()
	}
}

func (e *Engine) Stop() {
	close(e.stop)
}

func (e *Engine) BinPath() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bin
}

func (e *Engine) scanLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-ticker.C:
			settings := e.state.GetSettings()
			if !settings.AutoScan {
				continue
			}
			interval := time.Duration(settings.ScanIntervalSec) * time.Second
			e.mu.Lock()
			due := time.Since(e.lastScan) >= interval
			e.mu.Unlock()
			if due {
				e.ScanNow()
			}
		}
	}
}

func (e *Engine) ScanNow() {
	go e.scan()
}

func (e *Engine) scan() {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()

	e.mu.Lock()
	e.scanning = true
	e.foundLast = 0
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.scanning = false
		e.lastScan = time.Now()
		e.mu.Unlock()
	}()

	settings := e.state.GetSettings()
	for _, dir := range settings.WatchDirs {
		dir = filepath.Clean(dir)
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			continue
		}
		e.scanDir(dir, settings)
	}
}

func (e *Engine) scanDir(root string, settings Settings) {
	stable := time.Duration(settings.StableSeconds) * time.Second
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if partialFileName(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 512 {
			return nil
		}
		if skipExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if isSecondaryVolume(path) {
			return nil
		}
		if e.state.IsProcessed(path, info.Size(), info.ModTime().UnixNano()) {
			return nil
		}
		if settings.OnlyNew && settings.MonitorSince > 0 {
			ct := fileCreationTime(path)
			if ct > 0 && ct < settings.MonitorSince {
				return nil
			}
		}
		kind, err := detectArchive(path)
		if err != nil || kind == "" {
			return nil
		}

		firstVolume, _ := isFirstVolumePath(path)
		if firstVolume {
			if wt := e.state.WaitingVolumeTask(path); wt != nil {
				if !e.volumeSetReady(path, settings) {
					return nil
				}
				sig := volumeSignature(path)
				if sig == wt.VolumeSig {
					return nil
				}
				e.state.UpdateTask(wt.ID, func(t *Task) {
					t.Status = statusQueued
					t.VolumeSig = sig
					t.Message = "检测到新的分卷，重新处理"
					t.Tried = 0
					t.Progress = 0
				})
				e.state.AddLog(wt.ID, "检测到新的分卷，重新处理")
				e.enqueueTask(wt.ID)
				return nil
			}
		}

		if e.state.HasTaskFor(path, info.Size(), info.ModTime().UnixNano()) {
			return nil
		}

		if firstVolume {
			if !e.volumeSetReady(path, settings) {
				return nil
			}
		} else {
			if !e.isStable(path, info.Size(), info.ModTime().UnixNano(), stable) {
				return nil
			}
			if settings.WaitForClose && fileOpenForWrite(path) {
				e.mu.Lock()
				e.skippedOpen++
				e.mu.Unlock()
				return nil
			}
		}
		if t := e.enqueue(path, 0, ""); t != nil {
			e.mu.Lock()
			e.foundLast++
			e.mu.Unlock()
		}
		return nil
	})
}

func (e *Engine) isStable(path string, size, modTime int64, required time.Duration) bool {
	if required <= 0 {
		return true
	}
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	obs, ok := e.observed[path]
	if !ok || obs.size != size || obs.modTime != modTime {
		e.observed[path] = observation{size: size, modTime: modTime, since: now}
		return false
	}
	return now.Sub(obs.since) >= required
}

func (e *Engine) volumeSetReady(path string, settings Settings) bool {
	parts := volumePartPaths(path)
	if len(parts) == 0 {
		return false
	}
	stable := time.Duration(settings.StableSeconds) * time.Second
	ready := true
	for _, p := range parts {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			ready = false
			continue
		}
		if !e.isStable(p, info.Size(), info.ModTime().UnixNano(), stable) {
			ready = false
		}
		if settings.WaitForClose && fileOpenForWrite(p) {
			ready = false
		}
	}
	return ready
}

func (e *Engine) enqueue(path string, depth int, parent string) *Task {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if e.state.IsProcessed(abs, info.Size(), info.ModTime().UnixNano()) {
		return nil
	}
	if e.state.HasTaskFor(abs, info.Size(), info.ModTime().UnixNano()) {
		return nil
	}
	e.mu.Lock()
	if e.inFlight[abs] {
		e.mu.Unlock()
		return nil
	}
	e.inFlight[abs] = true
	e.mu.Unlock()

	t := e.state.NewTask(abs, info.Size(), info.ModTime().UnixNano(), depth, parent)
	e.state.AddLog(t.ID, "已加入队列")
	e.queue <- t.ID
	return t
}

func (e *Engine) enqueueTask(id string) {
	t := e.state.Task(id)
	if t == nil {
		return
	}
	e.mu.Lock()
	if e.inFlight[t.Archive] {
		e.mu.Unlock()
		return
	}
	e.inFlight[t.Archive] = true
	e.mu.Unlock()
	e.queue <- id
}

func (e *Engine) worker() {
	for {
		select {
		case <-e.stop:
			return
		case id := <-e.queue:
			t := e.state.Task(id)
			if t == nil {
				continue
			}
			e.process(t)
			e.mu.Lock()
			delete(e.inFlight, t.Archive)
			e.mu.Unlock()
		}
	}
}

func (e *Engine) process(t *Task) {
	id := t.ID
	bin := e.BinPath()
	if bin == "" {
		e.fail(id, "未找到 7-Zip 可执行文件，无法解压")
		return
	}

	e.state.UpdateTask(id, func(t *Task) {
		t.Status = statusAnalyzing
		t.Message = "正在识别压缩包"
		t.Progress = 0
	})

	kind, err := detectArchive(t.Archive)
	if err != nil {
		e.fail(id, "读取文件失败: "+err.Error())
		return
	}
	if kind == "" {
		e.state.UpdateTask(id, func(t *Task) {
			t.Status = statusSkipped
			t.Message = "不是可识别的压缩包"
		})
		e.markProcessed(t.Archive, id)
		return
	}
	e.state.UpdateTask(id, func(t *Task) { t.Kind = kind; t.Message = "正在读取压缩包信息" })

	info, err := inspectArchive(bin, t.Archive)
	if err != nil {
		if errors.Is(err, errIncompleteVolumes) {
			sig := volumeSignature(t.Archive)
			e.state.UpdateTask(id, func(t *Task) {
				t.Status = statusWaitingVolume
				t.VolumeSig = sig
				t.Message = "分卷不完整，等待后续分卷"
				t.Progress = 0
			})
			e.state.AddLog(id, "分卷不完整，等待后续分卷")
			return
		}
		e.fail(id, err.Error())
		return
	}

	if info.Encrypted {
		e.state.UpdateTask(id, func(t *Task) {
			t.Message = "检测到加密，正在匹配密码"
		})
		password := t.Password
		matched := false
		if password != "" {
			if ok, _ := testPassword(bin, t.Archive, info, password); ok {
				matched = true
				e.state.AddLog(id, "沿用已验证的密码")
			}
		}
		settings := e.state.GetSettings()
		candidates := []string{}
		if !matched {
			candidates = buildCandidates(t.Archive, info, settings, e.state.LearnedPasswords())
			e.state.AddLog(id, fmt.Sprintf("检测到加密压缩包，生成 %d 个候选密码", len(candidates)))
		}
		found := matched
		for i, pw := range candidates {
			e.state.UpdateTask(id, func(t *Task) {
				t.Tried = i + 1
				t.Message = fmt.Sprintf("正在尝试候选密码 %d/%d", i+1, len(candidates))
			})
			ok, _ := testPassword(bin, t.Archive, info, pw)
			if ok {
				found = true
				password = pw
				e.state.LearnPassword(pw)
				e.state.UpdateTask(id, func(t *Task) {
					t.Password = pw
					t.Message = "密码匹配成功"
				})
				e.state.AddLog(id, "密码匹配成功")
				break
			}
			if i > 0 && i%20 == 0 {
				e.state.AddLog(id, fmt.Sprintf("已尝试 %d 个密码", i+1))
			}
		}
		if !found {
			e.state.UpdateTask(id, func(t *Task) {
				t.Status = statusWaitingPassword
				t.Message = fmt.Sprintf("已尝试 %d 个候选密码，需要手动输入密码", len(candidates))
			})
			e.state.AddLog(id, "自动匹配失败，等待手动输入密码")
			return
		}
		t.Password = password
	}

	for _, entry := range info.Entries {
		if unsafeEntry(entry.Name) {
			e.fail(id, "压缩包包含不安全路径，已拒绝解压: "+entry.Name)
			return
		}
	}

	parent := filepath.Dir(t.Archive)
	target := filepath.Join(parent, targetBaseName(t.Archive))
	scanRoot := target
	if info.CommonRoot != "" {
		target = parent
		scanRoot = filepath.Join(parent, info.CommonRoot)
	}
	if filepath.Clean(target) == filepath.Clean(t.Archive) {
		target = filepath.Join(parent, targetBaseName(t.Archive)+"_解压")
		scanRoot = target
	}

	e.state.UpdateTask(id, func(t *Task) {
		t.Status = statusExtracting
		t.Target = target
		t.Progress = 0
		t.Message = "正在解压"
	})
	e.state.AddLog(id, "输出目录: "+target)

	lastProgress := time.Now()
	err = extractArchive(bin, t.Archive, target, t.Password, func(p int) {
		if time.Since(lastProgress) < 700*time.Millisecond && p < 100 {
			return
		}
		lastProgress = time.Now()
		e.state.UpdateTask(id, func(t *Task) { t.Progress = p })
	})
	if err != nil {
		e.fail(id, err.Error())
		return
	}

	e.state.UpdateTask(id, func(t *Task) {
		t.Status = statusDone
		t.Progress = 100
		t.Message = "解压完成"
	})
	e.state.AddLog(id, "解压完成")
	e.markProcessed(t.Archive, id)

	if info.HeaderEncrypted && scanRoot == target {
		// Header-encrypted archives have no entry list, so scan the output dir directly.
		scanRoot = target
	}
	e.scanNested(scanRoot, t.Depth+1, id)
}

func (e *Engine) fail(id, msg string) {
	e.state.UpdateTask(id, func(t *Task) {
		t.Status = statusFailed
		t.Message = msg
	})
	e.state.AddLog(id, "失败: "+msg)
}

func (e *Engine) markProcessed(path, taskID string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	e.state.MarkProcessed(path, info.Size(), info.ModTime().UnixNano(), taskID)
}

func (e *Engine) scanNested(root string, depth int, parent string) {
	settings := e.state.GetSettings()
	if depth > settings.ExtractDepth {
		return
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDirName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if partialFileName(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 512 {
			return nil
		}
		if skipExt[strings.ToLower(filepath.Ext(path))] || isSecondaryVolume(path) {
			return nil
		}
		kind, err := detectArchive(path)
		if err != nil || kind == "" {
			return nil
		}
		e.enqueue(path, depth, parent)
		return nil
	})
}

func (e *Engine) Retry(id string) error {
	t := e.state.Task(id)
	if t == nil {
		return fmt.Errorf("任务不存在")
	}
	e.state.UpdateTask(id, func(t *Task) {
		t.Status = statusQueued
		t.Message = "等待重新处理"
		t.Progress = 0
		t.Tried = 0
	})
	e.state.AddLog(id, "手动重试")
	e.enqueueTask(id)
	return nil
}

func (e *Engine) SubmitPassword(id, pw string) error {
	pw = strings.TrimSpace(pw)
	if pw == "" {
		return fmt.Errorf("密码不能为空")
	}
	t := e.state.Task(id)
	if t == nil {
		return fmt.Errorf("任务不存在")
	}
	bin := e.BinPath()
	info, err := inspectArchive(bin, t.Archive)
	if err != nil {
		return err
	}
	if !info.Encrypted {
		e.state.UpdateTask(id, func(t *Task) {
			t.Status = statusQueued
			t.Password = ""
			t.Message = "该压缩包未加密，重新处理"
		})
		e.enqueueTask(id)
		return nil
	}
	ok, _ := testPassword(bin, t.Archive, info, pw)
	if !ok {
		return fmt.Errorf("密码不正确")
	}
	e.state.LearnPassword(pw)
	e.state.UpdateTask(id, func(t *Task) {
		t.Status = statusQueued
		t.Password = pw
		t.Message = "密码已验证，等待解压"
	})
	e.state.AddLog(id, "手动密码验证成功")
	e.enqueueTask(id)
	return nil
}

func (e *Engine) Status() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{
		"scanning":     e.scanning,
		"last_scan":    e.lastScan,
		"last_found":   e.foundLast,
		"skipped_open": e.skippedOpen,
		"sevenzip":     e.bin,
		"started_at":   e.startedAt,
		"queue_len":    len(e.queue),
		"in_progress":  len(e.inFlight),
	}
}

func skipDirName(name string) bool {
	switch strings.ToLower(name) {
	case "@eadir", "#recycle", "@recycle", "$recycle.bin", "system volume information",
		".@__thumb", "@appstore", "@appdata", "@appconf", "@apphome", "@appcenter",
		"@apptemp", "@appmeta", "__macosx", "node_modules":
		return true
	}
	return strings.HasPrefix(name, ".")
}
