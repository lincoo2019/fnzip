package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	statusQueued          = "queued"
	statusAnalyzing       = "analyzing"
	statusWaitingPassword = "waiting_password"
	statusWaitingVolume   = "waiting_volume"
	statusExtracting      = "extracting"
	statusDone            = "done"
	statusFailed          = "failed"
	statusSkipped         = "skipped"
)

type Settings struct {
	WatchDirs        []string `json:"watch_dirs"`
	AutoScan         bool     `json:"auto_scan"`
	ScanIntervalSec  int      `json:"scan_interval_sec"`
	ExtractDepth     int      `json:"extract_depth"`
	StableSeconds    int      `json:"stable_seconds"`
	WaitForClose     bool     `json:"wait_for_close"`
	OnlyNew          bool     `json:"only_new"`
	MonitorSince     int64    `json:"monitor_since"`
	MaxPasswordTries int      `json:"max_password_tries"`
	PasswordBook     []string `json:"password_book"`
	SevenZip         string   `json:"sevenzip"`
}

func defaultSettings() Settings {
	return Settings{
		WatchDirs:        []string{},
		AutoScan:         true,
		ScanIntervalSec:  60,
		ExtractDepth:     3,
		StableSeconds:    20,
		WaitForClose:     true,
		OnlyNew:          true,
		MaxPasswordTries: 300,
		PasswordBook:     []string{},
	}
}

type Task struct {
	ID          string    `json:"id"`
	Archive     string    `json:"archive"`
	ArchiveSize int64     `json:"archive_size"`
	ArchiveMod  int64     `json:"archive_mod"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	Password    string    `json:"password,omitempty"`
	Target      string    `json:"target,omitempty"`
	Depth       int       `json:"depth"`
	ParentID    string    `json:"parent_id,omitempty"`
	Progress    int       `json:"progress"`
	Tried       int       `json:"tried"`
	VolumeSig   string    `json:"volume_sig,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Logs        []string  `json:"logs"`
}

type PasswordRecord struct {
	Password string    `json:"password"`
	Hits     int       `json:"hits"`
	LastUsed time.Time `json:"last_used"`
}

type ProcessedRecord struct {
	Size    int64     `json:"size"`
	ModTime int64     `json:"mod_time"`
	DoneAt  time.Time `json:"done_at"`
	TaskID  string    `json:"task_id"`
}

type State struct {
	mu           sync.RWMutex
	file         string
	passwordFile string
	dirty        bool
	Settings     Settings                   `json:"settings"`
	Tasks        []*Task                    `json:"tasks"`
	Learned      []PasswordRecord           `json:"learned"`
	Processed    map[string]ProcessedRecord `json:"processed"`
}

func LoadState(dataDir string) (*State, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &State{
		file:         filepath.Join(dataDir, "state.json"),
		passwordFile: filepath.Join(dataDir, "passwords.txt"),
		Settings:     defaultSettings(),
		Processed:    map[string]ProcessedRecord{},
	}
	firstRun := false
	if data, err := os.ReadFile(s.file); err == nil {
		if err := json.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("读取状态文件失败: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取状态文件失败: %w", err)
	} else {
		firstRun = true
		if raw, err := os.ReadFile(filepath.Join(dataDir, "watch_dirs.txt")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					s.Settings.WatchDirs = append(s.Settings.WatchDirs, line)
				}
			}
		}
	}
	s.normalize()
	if s.Settings.MonitorSince <= 0 {
		s.Settings.MonitorSince = time.Now().Unix()
		s.dirty = true
	}
	if raw, err := os.ReadFile(s.passwordFile); err == nil {
		s.Settings.PasswordBook = parsePasswordLines(string(raw))
	}
	if firstRun && len(s.Settings.WatchDirs) == 0 {
		s.Settings.WatchDirs = detectDefaultDirs()
	}
	go s.saver()
	return s, nil
}

func detectDefaultDirs() []string {
	if dirs := existingBaiduDirs(); len(dirs) > 0 {
		return dirs[:1]
	}
	for _, p := range []string{
		"/vol1/1000/downloads", "/vol1/1000/Downloads", "/vol1/1000/下载",
		"/vol1/downloads", "/vol1/Downloads", "/vol1/下载",
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return []string{p}
		}
	}
	return []string{}
}

func (s *State) normalize() {
	d := defaultSettings()
	if len(s.Settings.WatchDirs) == 0 {
		s.Settings.WatchDirs = []string{}
	}
	if s.Settings.ScanIntervalSec < 10 {
		s.Settings.ScanIntervalSec = d.ScanIntervalSec
	}
	if s.Settings.StableSeconds < 5 {
		s.Settings.StableSeconds = d.StableSeconds
	}
	if s.Settings.ExtractDepth < 0 {
		s.Settings.ExtractDepth = d.ExtractDepth
	}
	if s.Settings.MaxPasswordTries < 10 {
		s.Settings.MaxPasswordTries = d.MaxPasswordTries
	}
	if s.Settings.PasswordBook == nil {
		s.Settings.PasswordBook = []string{}
	}
	if s.Processed == nil {
		s.Processed = map[string]ProcessedRecord{}
	}
	if s.Tasks == nil {
		s.Tasks = []*Task{}
	}
}

func (s *State) saver() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		if !s.dirty {
			s.mu.Unlock()
			continue
		}
		s.dirty = false
		data, err := json.MarshalIndent(s, "", "  ")
		file := s.file
		s.mu.Unlock()
		if err != nil {
			continue
		}
		tmp := file + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			s.mu.Lock()
			s.dirty = true
			s.mu.Unlock()
			continue
		}
		if err := os.Rename(tmp, file); err != nil {
			s.mu.Lock()
			s.dirty = true
			s.mu.Unlock()
		}
	}
}

func (s *State) markDirtyLocked() {
	s.dirty = true
}

func (s *State) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.Settings
	out.WatchDirs = append([]string(nil), s.Settings.WatchDirs...)
	out.PasswordBook = append([]string(nil), s.Settings.PasswordBook...)
	return out
}

func (s *State) SetSevenZip(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Settings.SevenZip == path {
		return
	}
	s.Settings.SevenZip = path
	s.markDirtyLocked()
}

func (s *State) SetSettings(in Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.WatchDirs = cleanStringList(in.WatchDirs)
	if in.ScanIntervalSec < 10 {
		in.ScanIntervalSec = 60
	}
	if in.StableSeconds < 5 {
		in.StableSeconds = 20
	}
	if in.ExtractDepth < 0 {
		in.ExtractDepth = 3
	}
	in.ExtractDepth = min(in.ExtractDepth, 8)
	if in.MaxPasswordTries < 10 {
		in.MaxPasswordTries = 300
	}
	in.MaxPasswordTries = min(in.MaxPasswordTries, 2000)
	in.PasswordBook = cleanStringList(in.PasswordBook)
	if in.MonitorSince <= 0 {
		in.MonitorSince = s.Settings.MonitorSince
	}
	s.Settings = in
	s.writePasswordFileLocked()
	s.markDirtyLocked()
}

func (s *State) ResetBaseline() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	s.Settings.MonitorSince = now
	s.markDirtyLocked()
	return now
}

func (s *State) writePasswordFileLocked() {
	if s.passwordFile == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.passwordFile), 0o755)
	content := ""
	if len(s.Settings.PasswordBook) > 0 {
		content = strings.Join(s.Settings.PasswordBook, "\n") + "\n"
	}
	_ = os.WriteFile(s.passwordFile, []byte(content), 0o600)
}

func (s *State) AddPasswords(list []string) (Settings, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.Settings.PasswordBook)
	merged := append([]string(nil), s.Settings.PasswordBook...)
	merged = append(merged, list...)
	s.Settings.PasswordBook = cleanStringList(merged)
	s.writePasswordFileLocked()
	s.markDirtyLocked()
	out := s.Settings
	out.WatchDirs = append([]string(nil), s.Settings.WatchDirs...)
	out.PasswordBook = append([]string(nil), s.Settings.PasswordBook...)
	return out, len(s.Settings.PasswordBook) - before
}

func (s *State) DeleteLearned(pw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Learned {
		if s.Learned[i].Password == pw {
			s.Learned = append(s.Learned[:i], s.Learned[i+1:]...)
			s.markDirtyLocked()
			return
		}
	}
}

func (s *State) ClearLearned() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Learned = nil
	s.markDirtyLocked()
}

func (s *State) NewTask(archive string, size, modTime int64, depth int, parentID string) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Task{
		ID:          newTaskID(),
		Archive:     archive,
		ArchiveSize: size,
		ArchiveMod:  modTime,
		Status:      statusQueued,
		Depth:       depth,
		ParentID:    parentID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Logs:        []string{},
	}
	s.Tasks = append(s.Tasks, t)
	if len(s.Tasks) > 600 {
		s.Tasks = s.Tasks[len(s.Tasks)-600:]
	}
	s.markDirtyLocked()
	return copyTask(t)
}

func (s *State) UpdateTask(id string, fn func(*Task)) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Tasks {
		if t.ID == id {
			fn(t)
			t.UpdatedAt = time.Now()
			s.markDirtyLocked()
			return copyTask(t)
		}
	}
	return nil
}

func (s *State) Task(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.Tasks {
		if t.ID == id {
			return copyTask(t)
		}
	}
	return nil
}

func (s *State) AddLog(id string, line string) {
	s.UpdateTask(id, func(t *Task) {
		line = time.Now().Format("15:04:05") + "  " + line
		t.Logs = append(t.Logs, line)
		if len(t.Logs) > 200 {
			t.Logs = t.Logs[len(t.Logs)-200:]
		}
	})
}

func (s *State) HasOpenTask(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.Tasks {
		if t.Archive != path {
			continue
		}
		switch t.Status {
		case statusQueued, statusAnalyzing, statusExtracting, statusWaitingPassword:
			return true
		}
	}
	return false
}

func (s *State) HasTaskFor(path string, size, modTime int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.Tasks {
		if t.Archive == path && t.ArchiveSize == size && t.ArchiveMod == modTime {
			return true
		}
	}
	return false
}

func (s *State) WaitingVolumeTask(path string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.Tasks {
		if t.Archive == path && t.Status == statusWaitingVolume {
			return copyTask(t)
		}
	}
	return nil
}

func (s *State) IsProcessed(path string, size, modTime int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.Processed[path]
	return ok && p.Size == size && p.ModTime == modTime
}

func (s *State) MarkProcessed(path string, size, modTime int64, taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Processed[path] = ProcessedRecord{
		Size:    size,
		ModTime: modTime,
		DoneAt:  time.Now(),
		TaskID:  taskID,
	}
	if len(s.Processed) > 4000 {
		type pair struct {
			path string
			at   time.Time
		}
		all := make([]pair, 0, len(s.Processed))
		for p, r := range s.Processed {
			all = append(all, pair{p, r.DoneAt})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
		for i := 0; i < len(all)-3000; i++ {
			delete(s.Processed, all[i].path)
		}
	}
	s.markDirtyLocked()
}

func (s *State) LearnedPasswords() []PasswordRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]PasswordRecord(nil), s.Learned...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].LastUsed.After(out[j].LastUsed)
	})
	return out
}

func (s *State) LearnPassword(pw string) {
	if pw == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Learned {
		if s.Learned[i].Password == pw {
			s.Learned[i].Hits++
			s.Learned[i].LastUsed = time.Now()
			s.markDirtyLocked()
			return
		}
	}
	s.Learned = append(s.Learned, PasswordRecord{Password: pw, Hits: 1, LastUsed: time.Now()})
	if len(s.Learned) > 300 {
		sort.Slice(s.Learned, func(i, j int) bool {
			if s.Learned[i].Hits != s.Learned[j].Hits {
				return s.Learned[i].Hits > s.Learned[j].Hits
			}
			return s.Learned[i].LastUsed.After(s.Learned[j].LastUsed)
		})
		s.Learned = s.Learned[:300]
	}
	s.markDirtyLocked()
}

func (s *State) ResetInterrupted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, t := range s.Tasks {
		switch t.Status {
		case statusQueued, statusAnalyzing, statusExtracting:
			t.Status = statusQueued
			t.Progress = 0
			t.Message = "等待处理"
			t.UpdatedAt = time.Now()
			ids = append(ids, t.ID)
		}
	}
	if len(ids) > 0 {
		s.markDirtyLocked()
	}
	return ids
}

func (s *State) Snapshot() (Settings, []*Task, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	settings := s.Settings
	settings.WatchDirs = append([]string(nil), s.Settings.WatchDirs...)
	settings.PasswordBook = append([]string(nil), s.Settings.PasswordBook...)
	tasks := make([]*Task, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		tasks = append(tasks, copyTask(t))
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	return settings, tasks, len(s.Learned)
}

func (s *State) DeleteTask(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			s.markDirtyLocked()
			return
		}
	}
}

func copyTask(t *Task) *Task {
	out := *t
	out.Logs = append([]string(nil), t.Logs...)
	return &out
}

func cleanStringList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = trimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end {
		c := s[start]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '"' || c == '\'' {
			start++
			continue
		}
		break
	}
	for end > start {
		c := s[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '"' || c == '\'' {
			end--
			continue
		}
		break
	}
	return s[start:end]
}
