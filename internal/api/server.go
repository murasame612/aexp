package api

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	store    store.Store
	executor *executor.Executor
	monitor  *monitor.Manager
	logger   *slog.Logger
	hub      *WSHub
	apiToken string
}

// NewServer creates a new API server.
func NewServer(s store.Store, exec *executor.Executor, mon *monitor.Manager, logger *slog.Logger, apiToken string) *Server {
	return &Server{
		store:    s,
		executor: exec,
		monitor:  mon,
		logger:   logger,
		hub:      NewWSHub(),
		apiToken: apiToken,
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
			r.Post("/{id}/status-check", s.handleStatusCheck)
		})

		// Agent Events
		r.Get("/agent-events", s.handleListAgentEvents)

		// Exec (one-shot remote command)
		r.Post("/exec", s.handleExec)
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resources, _ := s.store.ListResources(ctx)
	runs, _ := s.store.ListRuns(ctx, store.RunFilter{})

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

	var update store.Resource
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	update.ID = existing.ID
	update.CreatedAt = existing.CreatedAt
	if update.Name == "" {
		update.Name = existing.Name
	}
	if update.Type == "" {
		update.Type = existing.Type
	}
	if update.Host == "" {
		update.Host = existing.Host
	}
	if update.Port == 0 {
		update.Port = existing.Port
	}
	if update.User == "" {
		update.User = existing.User
	}
	if update.RootDir == "" {
		update.RootDir = existing.RootDir
	}

	if err := s.store.UpdateResource(r.Context(), &update); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
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
	if limit == 0 {
		limit = 200
	}

	lines, err := s.store.GetLogLines(r.Context(), id, source, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	count, _ := s.store.CountLogLines(r.Context(), id, source)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id":      id,
		"source":      source,
		"total_lines": count,
		"offset":      offset,
		"limit":       limit,
		"lines":       lines,
	})
}

func (s *Server) handleGetSummary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	lines, err := s.executor.GetLogSnapshot(r.Context(), id, "stdout", 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LOG_READ_FAILED", err.Error())
		return
	}
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

// --- WebSocket ---

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ws upgrade", "error", err)
		return
	}
	defer conn.Close()

	// Send initial snapshot
	lastLines, _ := s.executor.GetLogSnapshot(r.Context(), id, "stdout", 200)
	for _, line := range lastLines {
		conn.WriteJSON(map[string]interface{}{
			"type":    "log.line",
			"run_id":  id,
			"source":  line.Source,
			"line_no": line.LineNo,
			"content": line.Content,
		})
	}

	// Stream new lines
	logCh, err := s.executor.TailLogs(r.Context(), id, "stdout", 0)
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
				"type":        "resource.snapshot",
				"resource_id": snap.ResourceID,
				"timestamp":   snap.Timestamp,
				"cpu_percent": snap.CPUPercent,
				"mem_used_mb": snap.MemUsedMB,
				"mem_total_mb": snap.MemTotalMB,
				"gpu_json":    snap.GPUJSON,
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

		auth := r.Header.Get("Authorization")
		if auth == "" {
			writeError(w, http.StatusUnauthorized, "NO_AUTH", "Authorization header required")
			return
		}

		// Support "Bearer <token>" or just "<token>"
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != s.apiToken {
			writeError(w, http.StatusUnauthorized, "INVALID_TOKEN", "Invalid API token")
			return
		}

		next.ServeHTTP(w, r)
	})
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
