package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed web
var embeddedWeb embed.FS

type Server struct {
	state *State
	eng   *Engine
	web   string
}

func NewServer(state *State, eng *Engine, webDir string) *Server {
	return &Server{state: state, eng: eng, web: webDir}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/browse", s.handleBrowse)
	mux.HandleFunc("POST /api/settings", s.handleSettings)
	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("POST /api/baseline/reset", s.handleBaselineReset)
	mux.HandleFunc("POST /api/passwords/import", s.handlePasswordImport)
	mux.HandleFunc("POST /api/learned/delete", s.handleLearnedDelete)
	mux.HandleFunc("POST /api/learned/clear", s.handleLearnedClear)
	mux.HandleFunc("GET /api/baidu-dirs", s.handleBaiduDirs)
	mux.HandleFunc("POST /api/tasks/{id}/password", s.handlePassword)
	mux.HandleFunc("POST /api/tasks/{id}/retry", s.handleRetry)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": appVersion})
	})

	if s.web != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(s.web)))
		return mux
	}
	sub, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	return mux
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	settings, tasks, learned := s.state.Snapshot()
	stats := map[string]int{
		statusQueued: 0, statusAnalyzing: 0, statusWaitingPassword: 0,
		statusWaitingVolume: 0, statusExtracting: 0, statusDone: 0,
		statusFailed: 0, statusSkipped: 0,
	}
	for _, t := range tasks {
		stats[t.Status]++
	}
	dirs := make([]map[string]any, 0, len(settings.WatchDirs))
	for _, d := range settings.WatchDirs {
		st, err := os.Stat(d)
		dirs = append(dirs, map[string]any{
			"path":   d,
			"exists": err == nil && st.IsDir(),
		})
	}
	engine := s.eng.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       appVersion,
		"now":           time.Now(),
		"settings":      settings,
		"tasks":         tasks,
		"stats":         stats,
		"learned":       s.state.LearnedPasswords(),
		"learned_count": learned,
		"watch_dirs":    dirs,
		"engine":        engine,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "参数格式错误")
		return
	}
	current := s.state.GetSettings()
	in.SevenZip = current.SevenZip
	in.MonitorSince = current.MonitorSince
	if in.PasswordBook == nil {
		in.PasswordBook = current.PasswordBook
	}
	added := addedPasswords(current.PasswordBook, cleanStringList(in.PasswordBook))
	if in.OnlyNew && in.MonitorSince <= 0 {
		in.MonitorSince = time.Now().Unix()
	}
	in.WatchDirs = cleanStringList(in.WatchDirs)
	for _, d := range in.WatchDirs {
		if !filepath.IsAbs(d) {
			writeErr(w, http.StatusBadRequest, "目录必须是绝对路径: "+d)
			return
		}
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			writeErr(w, http.StatusBadRequest, "目录不存在或不可访问: "+d)
			return
		}
	}
	if in.PasswordBook == nil {
		in.PasswordBook = []string{}
	}
	s.state.SetSettings(in)
	retried := 0
	if len(added) > 0 {
		retried = s.eng.RetryPasswordTasks()
	}
	if in.AutoScan {
		s.eng.ScanNow()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "retried": retried})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	s.eng.ScanNow()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBaselineReset(w http.ResponseWriter, r *http.Request) {
	now := s.state.ResetBaseline()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "monitor_since": now})
}

func (s *Server) handlePasswordImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "参数格式错误")
		return
	}
	path := filepath.Clean(strings.TrimSpace(body.Path))
	if path == "" || !filepath.IsAbs(path) {
		writeErr(w, http.StatusBadRequest, "请填写密码文件的绝对路径")
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusBadRequest, "密码文件不存在或不可访问")
		return
	}
	if st.Size() > 2*1024*1024 {
		writeErr(w, http.StatusBadRequest, "密码文件不能超过 2MB")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取密码文件失败")
		return
	}
	list := parsePasswordLines(decodeText(data))
	if len(list) == 0 {
		writeErr(w, http.StatusBadRequest, "文件中没有可用密码")
		return
	}
	_, added := s.state.AddPasswords(list)
	retried := 0
	if added > 0 {
		retried = s.eng.RetryPasswordTasks()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "total": len(list), "retried": retried})
}

func (s *Server) handleLearnedDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "参数格式错误")
		return
	}
	s.state.DeleteLearned(body.Password)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLearnedClear(w http.ResponseWriter, r *http.Request) {
	s.state.ClearLearned()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBaiduDirs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"dirs": existingBaiduDirs()})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "参数格式错误")
		return
	}
	if err := s.eng.SubmitPassword(id, body.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.Retry(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	s.state.DeleteTask(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	for _, blocked := range []string{"/proc", "/sys", "/dev", "/run"} {
		if path == blocked || strings.HasPrefix(path, blocked+"/") {
			writeErr(w, http.StatusBadRequest, "该目录不可浏览")
			return
		}
	}
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		writeErr(w, http.StatusBadRequest, "目录不存在或不可访问")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无法读取目录: "+err.Error())
		return
	}
	type item struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipDirName(name) {
			continue
		}
		items = append(items, item{Name: name, Path: filepath.Join(path, name)})
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	parent := ""
	if path != "/" && path != filepath.VolumeName(path)+"\\" {
		parent = filepath.Dir(path)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":   path,
		"parent": parent,
		"dirs":   items,
	})
}

func addedPasswords(old, new []string) []string {
	oldSet := map[string]bool{}
	for _, p := range old {
		oldSet[p] = true
	}
	var added []string
	for _, p := range new {
		if !oldSet[p] {
			added = append(added, p)
		}
	}
	return added
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
