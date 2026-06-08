package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/monitor"
	"github.com/ziwu/aexp/internal/store"
)

//go:embed static
var staticFS embed.FS

// Server is the HTTP API server.
type Server struct {
	store               store.Store
	executor            *executor.Executor
	monitor             *monitor.Manager
	logger              *slog.Logger
	hub                 *WSHub
	apiToken            string
	allowLoopbackNoAuth bool
}

// NewServer creates a new API server.
func NewServer(s store.Store, exec *executor.Executor, mon *monitor.Manager, logger *slog.Logger, apiToken string, allowLoopbackNoAuth bool) *Server {
	return &Server{
		store:               s,
		executor:            exec,
		monitor:             mon,
		logger:              logger,
		hub:                 NewWSHub(),
		apiToken:            apiToken,
		allowLoopbackNoAuth: allowLoopbackNoAuth,
	}
}

// Handler returns the configured HTTP handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(corsMiddleware)

	// API routes (with auth)
	r.Route("/api/v1", func(r chi.Router) {
		if s.apiToken != "" {
			r.Use(s.authMiddleware)
		}

		// Health (no auth required)
		r.Get("/health", s.handleHealth)
		r.Get("/stats", s.handleStats)

		// Resources
		r.Route("/resources", func(r chi.Router) {
			r.Get("/", s.handleListResources)
			r.Post("/", s.handleCreateResource)
			r.Get("/local-defaults", s.handleLocalResourceDefaults)
			r.Post("/local", s.handleCreateLocalResource)
			r.Get("/{id}", s.handleGetResource)
			r.Put("/{id}", s.handleUpdateResource)
			r.Delete("/{id}", s.handleDeleteResource)
			r.Get("/{id}/snapshots", s.handleListSnapshots)
			r.Post("/{id}/test", s.handleTestResource)
		})

		// Runs
		r.Route("/runs", func(r chi.Router) {
			r.Get("/", s.handleListRuns)
			r.Post("/", s.handleSubmitRun)
			r.Get("/{id}", s.handleGetRun)
			r.Post("/{id}/cancel", s.handleCancelRun)
			r.Get("/{id}/logs", s.handleGetLogs)
			r.Get("/{id}/summary", s.handleGetSummary)
			r.Get("/{id}/artifacts", s.handleListArtifacts)
			r.Get("/{id}/marks", s.handleListRunMarksForRun)
			r.Post("/{id}/marks", s.handleCreateRunMark)
			r.Post("/{id}/bookmark", s.handleSaveRunBookmark)
			r.Delete("/{id}/bookmark", s.handleDeleteRunBookmark)
			r.Post("/{id}/status-check", s.handleStatusCheck)
		})

		// Agent Events
		r.Get("/agent-events", s.handleListAgentEvents)

		// Run Marks
		r.Get("/run-marks", s.handleListRunMarks)
		r.Get("/run-marks/{id}", s.handleGetRunMark)

		// Human-curated favorite runs
		r.Get("/run-bookmarks", s.handleListRunBookmarks)

		// Exec (one-shot remote command)
		r.Post("/exec", s.handleExec)

		// Exec Events
		r.Get("/exec-events", s.handleListExecEvents)
		r.Get("/exec-events/{id}", s.handleGetExecEvent)
	})

	// WebSocket
	r.Get("/ws/runs/{id}/logs", s.handleWSLogs)
	r.Get("/ws/resources/{id}/metrics", s.handleWSMetrics)

	// Static files (embedded web UI)
	staticContent, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticContent))

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}

	// Use NotFound as fallback: serves index.html for SPA routes,
	// real static files for anything that matches.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		// Try to serve as static file first
		f, err := staticContent.Open(strings.TrimPrefix(req.URL.Path, "/"))
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, req)
			return
		}
		// Fallback to index.html (SPA)
		serveIndex(w, req)
	})

	return r
}

// --- Health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"os_type":  localOSType(),
		"hostname": host,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resources, _ := s.store.ListResources(ctx)
	runs, _ := s.store.ListRuns(ctx, store.RunFilter{})
	runs, _, _ = s.executor.RefreshRuns(ctx, runs, 2*time.Second)

	running := 0
	for _, run := range runs {
		if run.Status == store.RunStatusRunning {
			running++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_resources": len(resources),
		"total_runs":      len(runs),
		"active_runs":     running,
	})
}

// --- Resources ---

func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	resources, err := s.store.ListResources(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}

	type resourceWithSnapshot struct {
		store.Resource
		LatestSnapshot *store.Snapshot `json:"latest_snapshot,omitempty"`
	}

	result := make([]resourceWithSnapshot, 0, len(resources))
	for _, res := range resources {
		rws := resourceWithSnapshot{Resource: res}
		snap, _ := s.store.GetLatestSnapshot(r.Context(), res.ID)
		rws.LatestSnapshot = snap
		result = append(result, rws)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	var res store.Resource
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if res.Name == "" || res.Host == "" || res.RootDir == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "name, host, root_dir are required")
		return
	}

	if res.Type == "" {
		res.Type = store.ResourceTypeSSH
	}
	if res.Port == 0 {
		res.Port = 22
	}
	if res.User == "" {
		res.User = "root"
	}
	res.Status = store.ResourceStatusUnknown
	res.ID = genID("rsrc_")

	if err := s.store.CreateResource(r.Context(), &res); err != nil {
		writeError(w, http.StatusConflict, "CREATE_FAILED", err.Error())
		return
	}

	s.monitor.AddResource(&res)
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleLocalResourceDefaults(w http.ResponseWriter, r *http.Request) {
	res, err := localSSHResourceDefaults("", "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "LOCAL_DEFAULTS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCreateLocalResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		RootDir string `json:"root_dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	res, err := buildLocalSSHResource(req.Name, req.RootDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "LOCAL_RESOURCE_FAILED", err.Error())
		return
	}

	if err := testLocalSSH(r.Context(), &res); err != nil {
		writeError(w, http.StatusBadRequest, "LOCAL_SSH_FAILED", err.Error())
		return
	}

	if err := s.store.CreateResource(r.Context(), &res); err != nil {
		writeError(w, http.StatusConflict, "CREATE_FAILED", err.Error())
		return
	}

	s.monitor.AddResource(&res)
	writeJSON(w, http.StatusCreated, res)
}

func localSSHResourceDefaults(name string, rootDir string) (store.Resource, error) {
	if rootDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return store.Resource{}, fmt.Errorf("get current directory: %w", err)
		}
		rootDir = wd
	}
	if name == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "localhost"
		}
		name = "local-" + sanitizeLocalName(host)
	}
	userName := os.Getenv("USER")
	if userName == "" {
		userName = os.Getenv("LOGNAME")
	}
	if userName == "" {
		return store.Resource{}, fmt.Errorf("cannot determine local user")
	}

	keyPath := firstDefaultSSHKey()
	condaBase, condaInit := detectLocalConda()
	return store.Resource{
		Name:      name,
		Type:      store.ResourceTypeSSH,
		Host:      "127.0.0.1",
		OSType:    localOSType(),
		Port:      22,
		User:      userName,
		AuthRef:   keyPath,
		RootDir:   rootDir,
		CondaBase: condaBase,
		CondaInit: condaInit,
		Status:    store.ResourceStatusUnknown,
		Tags:      "local",
	}, nil
}

func buildLocalSSHResource(name string, rootDir string) (store.Resource, error) {
	res, err := localSSHResourceDefaults(name, rootDir)
	if err != nil {
		return store.Resource{}, err
	}
	if res.AuthRef == "" {
		return store.Resource{}, fmt.Errorf("no default SSH key found; create ~/.ssh/id_ed25519 or pass a normal SSH resource manually")
	}
	res.ID = genID("rsrc_")
	return res, nil
}

func testLocalSSH(ctx context.Context, r *store.Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		"-o", "StrictHostKeyChecking=no",
		"-i", r.AuthRef,
		"-p", fmt.Sprintf("%d", r.Port),
		r.User + "@" + r.Host,
		"echo ok",
	}
	out, err := osexec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh localhost failed: %w (%s). Enable Remote Login on macOS and ensure the public key is in ~/.ssh/authorized_keys", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstDefaultSSHKey() string {
	home, _ := os.UserHomeDir()
	for _, rel := range []string{".aexp/id_ed25519", ".ssh/id_ed25519", ".ssh/id_rsa"} {
		full := filepath.Join(home, rel)
		if _, err := os.Stat(full); err == nil {
			return full
		}
	}
	return ""
}

func detectLocalConda() (string, string) {
	conda, err := osexec.LookPath("conda")
	if err != nil {
		return "", ""
	}
	out, err := osexec.Command(conda, "info", "--base").Output()
	if err != nil {
		return "", ""
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return "", ""
	}
	return base, filepath.Join(base, "etc/profile.d/conda.sh")
}

func localOSType() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "linux":
		return "linux"
	default:
		return runtime.GOOS
	}
}

func sanitizeLocalName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "machine"
	}
	return out
}

func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res, err := s.store.GetResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if res == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
		return
	}

	snap, _ := s.store.GetLatestSnapshot(r.Context(), id)

	type resp struct {
		store.Resource
		LatestSnapshot *store.Snapshot `json:"latest_snapshot,omitempty"`
	}

	writeJSON(w, http.StatusOK, resp{Resource: *res, LatestSnapshot: snap})
}

func (s *Server) handleUpdateResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, _ := s.store.GetResource(r.Context(), id)
	if existing == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	hasField := func(name string) bool {
		_, ok := fields[name]
		return ok
	}

	var update store.Resource
	if err := json.Unmarshal(body, &update); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	update.ID = existing.ID
	update.CreatedAt = existing.CreatedAt
	if !hasField("name") {
		update.Name = existing.Name
	}
	if !hasField("type") {
		update.Type = existing.Type
	}
	if !hasField("host") {
		update.Host = existing.Host
	}
	if !hasField("os_type") {
		update.OSType = existing.OSType
	}
	if !hasField("port") {
		update.Port = existing.Port
	}
	if !hasField("user") {
		update.User = existing.User
	}
	if !hasField("auth_ref") {
		update.AuthRef = existing.AuthRef
	}
	if !hasField("socks_proxy") {
		update.SocksProxy = existing.SocksProxy
	}
	if !hasField("proxy_command") {
		update.ProxyCommand = existing.ProxyCommand
	}
	if !hasField("root_dir") {
		update.RootDir = existing.RootDir
	}
	if !hasField("conda_base") {
		update.CondaBase = existing.CondaBase
	}
	if !hasField("conda_init") {
		update.CondaInit = existing.CondaInit
	}
	if !hasField("conda_env") {
		update.CondaEnv = existing.CondaEnv
	}
	if !hasField("gpu_indices") {
		update.GPUIndices = existing.GPUIndices
	}
	if !hasField("tags") {
		update.Tags = existing.Tags
	}
	if !hasField("status") {
		update.Status = store.ResourceStatusUnknown
	}

	if err := s.store.UpdateResource(r.Context(), &update); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	if s.executor != nil && s.executor.Pool() != nil {
		s.executor.Pool().RemoveByHost(existing.Host, existing.Port)
		if existing.Host != update.Host || existing.Port != update.Port {
			s.executor.Pool().RemoveByHost(update.Host, update.Port)
		}
	}
	s.monitor.RemoveResource(id)
	s.monitor.AddResource(&update)
	writeJSON(w, http.StatusOK, update)
}

func (s *Server) handleDeleteResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.monitor.RemoveResource(id)
	if err := s.store.DeleteResource(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	snaps, err := s.store.ListSnapshots(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}

func (s *Server) handleTestResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res, _ := s.store.GetResource(r.Context(), id)
	if res == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
		return
	}

	pool := s.executor.Pool()
	_, _, err := pool.Exec(r.Context(), res.Host, res.Port, res.User, res.AuthRef, "echo ok", res.SocksProxy, res.ProxyCommand)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// --- Runs ---

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	filter := store.RunFilter{
		ResourceID: r.URL.Query().Get("resource"),
		Status:     r.URL.Query().Get("status"),
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		filter.Limit, _ = strconv.Atoi(limitStr)
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		filter.Offset, _ = strconv.Atoi(offsetStr)
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}

	runs, err := s.store.ListRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if !isFalseQuery(r.URL.Query().Get("refresh")) {
		runs, _, _ = s.executor.RefreshRuns(r.Context(), runs, 2*time.Second)
		if filter.Status != "" {
			filtered := runs[:0]
			for _, run := range runs {
				if run.Status == filter.Status {
					filtered = append(filtered, run)
				}
			}
			runs = filtered
		}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	var req executor.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	run, err := s.executor.Submit(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "SUBMIT_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}
	if run.Status == store.RunStatusRunning || run.Status == store.RunStatusStarting {
		if refreshed, err := s.executor.CheckRunStatus(r.Context(), id); err == nil && refreshed != nil {
			run = refreshed
		}
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.executor.Cancel(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, "CANCEL_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "stdout"
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	if limit > 10000 {
		limit = 10000
	}

	var lines []store.LogLine
	var total int
	var remote bool
	tailMode := parseBoolQuery(r.URL.Query().Get("tail"))
	if tailMode && offset == 0 {
		lines, total, remote = s.remoteLogLines(r.Context(), id, source, limit)
	}
	if !remote {
		lines, total, remote = s.fastLogLines(r.Context(), id, source, offset, limit)
	}
	firstLine, lastLine := logLineBounds(lines)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id":      id,
		"source":      source,
		"total_lines": total,
		"offset":      offset,
		"limit":       limit,
		"lines":       lines,
		"remote":      remote,
		"tail":        tailMode,
		"first_line":  firstLine,
		"last_line":   lastLine,
		"truncated":   firstLine > 1 && total > len(lines),
	})
}

func (s *Server) fastLogLines(ctx context.Context, runID string, source string, offset, limit int) ([]store.LogLine, int, bool) {
	lines, err := s.store.GetLogLines(ctx, runID, source, offset, limit)
	if err != nil {
		return nil, 0, false
	}
	count, _ := s.store.CountLogLines(ctx, runID, source)
	if count > 0 || offset > 0 {
		return lines, count, false
	}

	remoteLines, total, ok := s.remoteLogLines(ctx, runID, source, limit)
	if !ok {
		return lines, count, false
	}
	s.cacheRemoteLogLines(ctx, runID, source, remoteLines, total)
	return remoteLines, total, true
}

func (s *Server) remoteLogLines(ctx context.Context, runID string, source string, limit int) ([]store.LogLine, int, bool) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return nil, 0, false
	}
	resource, err := s.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil || resource.Status == store.ResourceStatusUnreachable {
		return nil, 0, false
	}

	remoteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	remoteLines, err := s.executor.GetLogSnapshot(remoteCtx, runID, source, limit)
	if err != nil {
		return nil, 0, false
	}
	cached := make([]store.LogLine, 0, len(remoteLines))
	for _, line := range remoteLines {
		cached = append(cached, store.LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  line.LineNo,
			Content: line.Content,
		})
	}
	total := 0
	if len(cached) > 0 {
		total = cached[len(cached)-1].LineNo
	}
	return cached, total, true
}

func logLineBounds(lines []store.LogLine) (int, int) {
	if len(lines) == 0 {
		return 0, 0
	}
	return lines[0].LineNo, lines[len(lines)-1].LineNo
}

func parseBoolQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isFalseQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func (s *Server) handleGetSummary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	lines, _, _ := s.fastLogLines(r.Context(), id, "stdout", 0, 50)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id": id,
		"lines":  lines,
	})
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	artifacts, err := s.store.ListArtifacts(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, artifacts)
}

func (s *Server) handleStatusCheck(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := s.executor.CheckRunStatus(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CHECK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// --- Agent Events ---

func (s *Server) handleListAgentEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	events, err := s.store.ListAgentEvents(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// --- Run Marks ---

func (s *Server) handleListRunMarks(w http.ResponseWriter, r *http.Request) {
	filter := runMarkFilterFromQuery(r)
	if filter.Limit == 0 {
		filter.Limit = 50
	}

	marks, err := s.store.ListRunMarks(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, marks)
}

func (s *Server) handleListRunMarksForRun(w http.ResponseWriter, r *http.Request) {
	filter := runMarkFilterFromQuery(r)
	filter.RunID = chi.URLParam(r, "id")
	if filter.Limit == 0 {
		filter.Limit = 50
	}

	marks, err := s.store.ListRunMarks(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, marks)
}

func (s *Server) handleGetRunMark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mark, err := s.store.GetRunMark(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if mark == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run mark not found")
		return
	}
	writeJSON(w, http.StatusOK, mark)
}

func (s *Server) handleCreateRunMark(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	var mark store.RunMark
	if err := json.NewDecoder(r.Body).Decode(&mark); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	mark.ID = genID("mark_")
	mark.RunID = runID
	mark.Actor = strings.TrimSpace(mark.Actor)
	mark.Kind = strings.TrimSpace(mark.Kind)
	mark.Title = strings.TrimSpace(mark.Title)
	mark.Reason = strings.TrimSpace(mark.Reason)
	mark.Evidence = strings.TrimSpace(mark.Evidence)
	if mark.Actor == "" {
		mark.Actor = "api"
	}
	if mark.Kind == "" {
		mark.Kind = "key_result"
	}
	if mark.Title == "" && mark.Reason == "" && mark.Evidence == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "title, reason, or evidence is required")
		return
	}

	if err := s.store.SaveRunMark(r.Context(), &mark); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mark)
}

func runMarkFilterFromQuery(r *http.Request) store.RunMarkFilter {
	filter := store.RunMarkFilter{
		RunID: r.URL.Query().Get("run_id"),
		Actor: r.URL.Query().Get("actor"),
		Kind:  r.URL.Query().Get("kind"),
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n > 0 {
			filter.Offset = n
		}
	}
	return filter
}

// --- Run Bookmarks ---

type runBookmarkView struct {
	ID        string     `json:"id"`
	RunID     string     `json:"run_id"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Run       *store.Run `json:"run,omitempty"`
}

func (s *Server) handleListRunBookmarks(w http.ResponseWriter, r *http.Request) {
	filter := store.RunBookmarkFilter{}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n > 0 {
			filter.Offset = n
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 100
	}

	bookmarks, err := s.store.ListRunBookmarks(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.enrichRunBookmarks(r.Context(), bookmarks))
}

func (s *Server) handleSaveRunBookmark(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	var req struct {
		Note string `json:"note"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}
	}

	existing, err := s.store.GetRunBookmark(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	bookmark := &store.RunBookmark{
		ID:    genID("bm_"),
		RunID: runID,
		Note:  strings.TrimSpace(req.Note),
	}
	if existing != nil {
		bookmark.ID = existing.ID
		bookmark.CreatedAt = existing.CreatedAt
	}

	if err := s.store.SaveRunBookmark(r.Context(), bookmark); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.enrichRunBookmark(r.Context(), *bookmark))
}

func (s *Server) handleDeleteRunBookmark(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	if err := s.store.DeleteRunBookmark(r.Context(), runID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) enrichRunBookmarks(ctx context.Context, bookmarks []store.RunBookmark) []runBookmarkView {
	views := make([]runBookmarkView, 0, len(bookmarks))
	for _, b := range bookmarks {
		views = append(views, s.enrichRunBookmark(ctx, b))
	}
	return views
}

func (s *Server) enrichRunBookmark(ctx context.Context, b store.RunBookmark) runBookmarkView {
	run, _ := s.store.GetRun(ctx, b.RunID)
	return runBookmarkView{
		ID:        b.ID,
		RunID:     b.RunID,
		Note:      b.Note,
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
		Run:       run,
	}
}

// --- Exec ---

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	var req executor.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if req.ResourceID == "" || req.Command == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "resource_id and command are required")
		return
	}

	// Extract actor from token (simplified: use "api" for now)
	req.Actor = "api"

	result, err := s.executor.Exec(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "EXEC_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// --- Exec Events ---

func (s *Server) handleListExecEvents(w http.ResponseWriter, r *http.Request) {
	filter := store.ExecEventFilter{}

	if resourceID := r.URL.Query().Get("resource_id"); resourceID != "" {
		filter.ResourceID = resourceID
	}
	if actor := r.URL.Query().Get("actor"); actor != "" {
		filter.Actor = actor
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n > 0 {
			filter.Offset = n
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}

	events, err := s.store.ListExecEvents(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleGetExecEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ev, err := s.store.GetExecEvent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if ev == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "exec event not found")
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// --- WebSocket ---

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(w, r) {
		return
	}

	id := chi.URLParam(r, "id")
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "stdout"
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ws upgrade", "error", err)
		return
	}
	defer conn.Close()

	if !isFalseQuery(r.URL.Query().Get("snapshot")) {
		lastLines, _ := s.executor.GetLogSnapshot(r.Context(), id, source, 200)
		for _, line := range lastLines {
			conn.WriteJSON(map[string]interface{}{
				"type":    "log.line",
				"run_id":  id,
				"source":  line.Source,
				"line_no": line.LineNo,
				"content": line.Content,
			})
		}
	}

	// Stream new lines
	logCh, err := s.executor.TailLogs(r.Context(), id, source, 0)
	if err != nil {
		conn.WriteJSON(map[string]string{"type": "error", "message": err.Error()})
		return
	}

	for line := range logCh {
		if err := conn.WriteJSON(map[string]interface{}{
			"type":    "log.line",
			"run_id":  line.RunID,
			"source":  line.Source,
			"line_no": line.LineNo,
			"content": line.Content,
		}); err != nil {
			break
		}
	}
}

func (s *Server) handleWSMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(w, r) {
		return
	}

	id := chi.URLParam(r, "id")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Poll and send snapshots
	// This is a simplified implementation - in production, use the hub
	res, _ := s.store.GetResource(r.Context(), id)
	if res == nil {
		conn.WriteJSON(map[string]string{"type": "error", "message": "resource not found"})
		return
	}

	for {
		snap, _ := s.store.GetLatestSnapshot(r.Context(), id)
		if snap != nil {
			conn.WriteJSON(map[string]interface{}{
				"type":         "resource.snapshot",
				"resource_id":  snap.ResourceID,
				"timestamp":    snap.Timestamp,
				"cpu_percent":  snap.CPUPercent,
				"mem_used_mb":  snap.MemUsedMB,
				"mem_total_mb": snap.MemTotalMB,
				"gpu_json":     snap.GPUJSON,
			})
		}

		// Check for close
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string, details string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"details": details,
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health check
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		if !s.authorizeRequest(w, r) {
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorizeRequest(w http.ResponseWriter, r *http.Request) bool {
	if s.apiToken == "" {
		return true
	}
	if s.allowLoopbackNoAuth && isLoopbackRequest(r) {
		return true
	}

	token := requestToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "NO_AUTH", "Authorization header or token query required")
		return false
	}
	if token != s.apiToken {
		writeError(w, http.StatusUnauthorized, "INVALID_TOKEN", "Invalid API token")
		return false
	}
	return true
}

func (s *Server) cacheRemoteLogLines(ctx context.Context, runID string, source string, lines []store.LogLine, total int) {
	if len(lines) == 0 {
		return
	}
	if lines[0].LineNo != 1 || total != len(lines) {
		return
	}
	count, err := s.store.CountLogLines(ctx, runID, source)
	if err != nil || count > 0 {
		return
	}

	cached := make([]store.LogLine, 0, len(lines))
	for _, line := range lines {
		cached = append(cached, store.LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  line.LineNo,
			Content: line.Content,
		})
	}
	_ = s.store.AppendLogLines(ctx, runID, cached)
}

func requestToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func genID(prefix string) string {
	id, _ := gonanoid.Generate("0123456789abcdefghijklmnopqrstuvwxyz", 12)
	return prefix + id
}

// WSHub manages WebSocket connections.
type WSHub struct{}

func NewWSHub() *WSHub { return &WSHub{} }
