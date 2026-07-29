package api

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/eventcache"
	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/monitor"
	printerservice "github.com/ziwu/aexp/internal/printer"
	releaseservice "github.com/ziwu/aexp/internal/release"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
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
	files               *filespace.Service
	transferPlanner     *transfer.Planner
	transfers           *transfer.Service
	printer             *printerservice.Manager
}

type ServerOption func(*Server)

func WithFileSpaceService(service *filespace.Service) ServerOption {
	return func(server *Server) { server.files = service }
}

func WithTransferServices(planner *transfer.Planner, service *transfer.Service) ServerOption {
	return func(server *Server) {
		server.transferPlanner = planner
		server.transfers = service
	}
}

func WithPrinterManager(manager *printerservice.Manager) ServerOption {
	return func(server *Server) { server.printer = manager }
}

type paginatedResponse[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type runSummaryPage struct {
	Items        []store.RunSummary `json:"items"`
	Total        int                `json:"total"`
	Limit        int                `json:"limit"`
	Offset       int                `json:"offset"`
	ChangeCursor int64              `json:"change_cursor"`
}

// NewServer creates a new API server.
func NewServer(s store.Store, exec *executor.Executor, mon *monitor.Manager, logger *slog.Logger, apiToken string, allowLoopbackNoAuth bool, options ...ServerOption) *Server {
	if exec != nil {
		if err := exec.ResumePendingSubmissions(context.Background()); err != nil && logger != nil {
			logger.Error("resume pending run submissions", "error", err)
		}
	}
	server := &Server{
		store:               s,
		executor:            exec,
		monitor:             mon,
		logger:              logger,
		hub:                 NewWSHub(),
		apiToken:            apiToken,
		allowLoopbackNoAuth: allowLoopbackNoAuth,
	}
	for _, option := range options {
		option(server)
	}
	return server
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
		r.Get("/printer/status", s.handlePrinterStatus)
		r.Patch("/printer/config", s.handlePrinterConfig)
		r.Post("/printer/test", s.handlePrinterTest)
		r.Get("/printer/jobs", s.handlePrinterJobs)
		r.Post("/printer/jobs/{id}/retry", s.handlePrinterRetry)

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
			r.Post("/{id}/refresh", s.handleRefreshResource)
			r.Post("/{id}/test", s.handleTestResource)
		})

		// Data center control plane. Dataset bytes live on storage targets or
		// compute-node caches; these endpoints return metadata only.
		r.Get("/storage-targets", s.handleListStorageTargets)
		r.Post("/storage-targets", s.handleSaveStorageTarget)
		r.Get("/storage-targets/{id}", s.handleGetStorageTarget)
		r.Put("/storage-targets/{id}", s.handleUpdateStorageTarget)
		r.Delete("/storage-targets/{id}", s.handleDeleteStorageTarget)
		r.Post("/storage-targets/{id}/test", s.handleTestStorageTarget)
		r.Get("/dataset-versions", s.handleListDatasetVersions)
		r.Get("/dataset-versions/{id}/materializations", s.handleListDatasetMaterializations)
		r.Get("/logical-roots", s.handleListLogicalRoots)
		r.Post("/logical-roots", s.handleSaveLogicalRoot)
		r.Put("/logical-roots/{id}", s.handleSaveLogicalRoot)
		r.Delete("/logical-roots/{id}", s.handleDeleteLogicalRoot)
		r.Post("/paths/resolve", s.handleResolveLogicalPath)
		r.Get("/paths/locate", s.handleLocateLogicalPath)
		r.Post("/paths/inspect", s.handleInspectLogicalPath)
		r.Get("/paths/list", s.handleListLogicalPath)
		r.Post("/paths/hash", s.handleHashLogicalPath)
		r.Post("/paths/ensure", s.handleEnsureLogicalPath)
		r.Get("/storage/stat", s.handleStorageStat)
		r.Get("/storage/list", s.handleStorageList)
		r.Get("/storage/locations", s.handleStorageLocations)
		r.Post("/storage/copy", s.handleStorageCopy)
		r.Post("/transfers/plan", s.handlePlanTransfer)
		r.Post("/transfers", s.handleCreateTransfer)
		r.Get("/transfers", s.handleListTransfers)
		r.Get("/transfers/{id}", s.handleGetTransfer)
		r.Post("/transfers/{id}/retry", s.handleRetryTransfer)
		r.Post("/transfers/{id}/cancel", s.handleCancelTransfer)

		// Runs
		r.Route("/runs", func(r chi.Router) {
			r.Get("/", s.handleListRuns)
			r.Post("/", s.handleSubmitRun)
			r.Get("/active", s.handleListActiveRunSummaries)
			r.Get("/summaries", s.handleListRunSummaries)
			r.Get("/changes", s.handleListRunChanges)
			r.Get("/changes/stream", s.handleRunChangeStream)
			r.Get("/{id}", s.handleGetRun)
			r.Put("/{id}/project", s.handleAssignRunProject)
			r.Post("/{id}/cancel", s.handleCancelRun)
			r.Post("/{id}/archive", s.handleArchiveRun)
			r.Post("/{id}/restore", s.handleRestoreRun)
			r.Delete("/{id}", s.handleDeleteRunLogically)
			r.Get("/{id}/logs", s.handleGetLogs)
			r.Get("/{id}/summary", s.handleGetSummary)
			r.Get("/{id}/artifacts", s.handleListArtifacts)
			r.Get("/{id}/artifact-collection", s.handleGetArtifactCollection)
			r.Post("/{id}/artifacts/collect", s.handleCollectArtifacts)
			r.Get("/{id}/manifest", s.handleGetRunManifest)
			r.Post("/{id}/snapshots", s.handleCreateEvidenceSnapshot)
			r.Get("/{id}/snapshots", s.handleListEvidenceSnapshots)
			r.Get("/{id}/data-bindings", s.handleGetRunDataBindings)
			r.Get("/{id}/journal", s.handleListProjectJournalForRun)
			r.Get("/{id}/marks", s.handleListRunMarksForRun)
			r.Post("/{id}/marks", s.handleCreateRunMark)
			r.Post("/{id}/bookmark", s.handleSaveRunBookmark)
			r.Delete("/{id}/bookmark", s.handleDeleteRunBookmark)
			r.Put("/{id}/project-card", s.handleSaveProjectRunCard)
			r.Put("/{id}/manual-project-category", s.handleAssignRunManualProjectCategory)
			r.Delete("/{id}/manual-project-category", s.handleUnassignRunManualProjectCategory)
			r.Post("/{id}/status-check", s.handleStatusCheck)
			r.Post("/{id}/freeze/plan", s.handlePlanRunFreeze)
			r.Post("/{id}/freezes", s.handleCreateRunFreeze)
			r.Get("/{id}/freezes", s.handleListRunFreezes)
			r.Get("/{id}/evidence-proposal", s.handleGetEvidenceGraphProposal)
			r.Post("/{id}/evidence-proposal", s.handleSubmitEvidenceGraphProposal)
			r.Post("/{id}/evidence-proposal/plan", s.handlePlanEvidenceGraphProposal)
			r.Post("/{id}/evidence-proposal/review", s.handleReviewEvidenceGraphProposal)
		})
		r.Get("/freezes/{id}", s.handleGetRunFreeze)
		r.Get("/freezes/{id}/manifest", s.handleGetRunFreezeManifest)
		r.Get("/snapshots/{id}", s.handleGetEvidenceSnapshot)
		r.Post("/snapshots/{id}/releases", s.handleCreateEvidenceRelease)
		r.Get("/snapshots/{id}/releases", s.handleListEvidenceReleases)
		r.Get("/releases/{id}", s.handleGetEvidenceRelease)

		// Agent Events
		r.Get("/agent-events", s.handleListAgentEvents)

		// Run Marks
		r.Get("/run-marks", s.handleListRunMarks)
		r.Get("/run-marks/{id}", s.handleGetRunMark)
		r.Get("/run-marks/{id}/attachments/{attachmentID}/blob", s.handleGetRunMarkAttachmentBlob)

		// Human-curated favorite runs
		r.Get("/run-bookmarks", s.handleListRunBookmarks)

		// Project-level experiment memory
		r.Get("/projects", s.handleListProjects)
		r.Get("/projects/{id}", s.handleGetProject)

		// Authoritative executable projects. Kept separate from the legacy
		// /projects evidence aggregation until clients migrate explicitly.
		r.Route("/project-definitions", func(r chi.Router) {
			r.Get("/", s.handleListProjectDefinitions)
			r.Post("/", s.handleSaveProjectDefinition)
			r.Get("/{id}", s.handleGetProjectDefinition)
			r.Put("/{id}", s.handleSaveProjectDefinition)
			r.Delete("/{id}", s.handleDeleteProjectDefinition)
			r.Get("/{id}/evidence-map", s.handleGetProjectEvidenceMap)
			r.Post("/{id}/evidence-map", s.handleEnsureProjectEvidenceMap)
			r.Post("/{id}/evidence-maps", s.handleCreateProjectEvidenceMap)
			r.Get("/{id}/evidence-proposals", s.handleListProjectEvidenceProposals)
			r.Post("/{id}/evidence-proposals", s.handleCreateProjectEvidenceProposal)
			r.Get("/{id}/journal", s.handleListProjectJournal)
			r.Post("/{id}/journal", s.handleCreateProjectJournalEntry)
			r.Get("/{id}/assets", s.handleListProjectAssets)
			r.Get("/{id}/targets", s.handleListProjectTargets)
			r.Post("/{id}/targets", s.handleSaveProjectTarget)
			r.Post("/{id}/targets/{targetID}/prepare-plan", s.handleProjectTargetPreparePlan)
			r.Post("/{id}/targets/{targetID}/prepare", s.handleProjectTargetPrepare)
		})
		r.Get("/project-targets/{id}", s.handleGetProjectTarget)
		r.Put("/project-targets/{id}", s.handleSaveProjectTarget)
		r.Delete("/project-targets/{id}", s.handleDeleteProjectTarget)
		r.Get("/journal-entries/{id}", s.handleGetProjectJournalEntry)
		r.Patch("/journal-entries/{id}/next-action", s.handleUpdateProjectJournalNextAction)
		r.Get("/manual-project-categories", s.handleListManualProjectCategories)
		r.Post("/manual-project-categories", s.handleCreateManualProjectCategory)
		r.Get("/manual-run-project-assignments", s.handleListManualRunProjectAssignments)

		// Experiment Matrix comparison workspaces
		r.Post("/run-comparisons/analyze", s.handleAnalyzeRunComparison)
		r.Get("/experiment-matrices", s.handleListExperimentMatrices)
		r.Post("/experiment-matrices", s.handleCreateExperimentMatrix)
		r.Get("/experiment-matrices/{id}", s.handleGetExperimentMatrix)
		r.Put("/experiment-matrices/{id}", s.handleUpdateExperimentMatrix)
		r.Delete("/experiment-matrices/{id}", s.handleDeleteExperimentMatrix)
		r.Put("/experiment-matrices/{id}/grid", s.handleSaveExperimentMatrixGrid)

		// Evidence Chain reasoning boards
		r.Get("/evidence-chains", s.handleListEvidenceChains)
		r.Post("/evidence-chains", s.handleCreateEvidenceChain)
		r.Get("/evidence-chains/{id}", s.handleGetEvidenceChain)
		r.Get("/evidence-chains/{id}/audit", s.handleAuditEvidenceChain)
		r.Put("/evidence-chains/{id}", s.handleUpdateEvidenceChain)
		r.Delete("/evidence-chains/{id}", s.handleDeleteEvidenceChain)
		r.Put("/evidence-chains/{id}/graph", s.handleSaveEvidenceChainGraph)
		r.Get("/evidence-chains/{id}/revisions", s.handleListEvidenceChainRevisions)
		r.Get("/evidence-chains/{id}/revisions/{revision}", s.handleGetEvidenceChainRevision)
		r.Post("/evidence-chains/{id}/promotion/plan", s.handlePlanEvidencePromotion)
		r.Post("/evidence-chains/{id}/promotions", s.handleCreateEvidencePromotion)
		r.Get("/evidence-run-candidates", s.handleListEvidenceRunCandidates)
		r.Get("/evidence-proposals/{id}", s.handleGetEvidenceProposal)
		r.Post("/evidence-proposals/{id}/plan", s.handlePlanEvidenceProposal)
		r.Post("/evidence-proposals/{id}/review", s.handleReviewEvidenceProposal)
		r.Post("/evidence-proposals/{id}/reroute", s.handleRerouteEvidenceProposal)

		// Exec (one-shot remote command)
		r.Post("/exec", s.handleExec)

		// Exec Events
		r.Get("/exec-events", s.handleListExecEvents)
		r.Get("/exec-events/{id}", s.handleGetExecEvent)
	})

	// WebSocket
	r.Get("/ws/runs/{id}/logs", s.handleWSLogs)
	r.Get("/ws/resources/{id}/metrics", s.handleWSMetrics)

	// React UI v2. Kept parallel to the legacy root dashboard so existing
	// deployments can validate the new app without changing their entrypoint.
	staticContent, _ := fs.Sub(staticFS, "static")
	uiV2Content, uiV2Err := fs.Sub(staticFS, "static/ui-v2")
	if uiV2Err == nil {
		uiV2Server := http.StripPrefix("/ui-v2", http.FileServer(http.FS(uiV2Content)))
		r.Get("/ui-v2", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui-v2/", http.StatusMovedPermanently)
		})
		r.Get("/ui-v2/*", func(w http.ResponseWriter, req *http.Request) {
			name := strings.TrimPrefix(req.URL.Path, "/ui-v2/")
			w.Header().Set("Cache-Control", uiV2CacheControl(name))
			if name != "" {
				if f, err := uiV2Content.Open(name); err == nil {
					f.Close()
					uiV2Server.ServeHTTP(w, req)
					return
				}
			}
			data, err := staticFS.ReadFile("static/ui-v2/index.html")
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
		})
	}

	// Static files (embedded legacy web UI)
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
		if strings.HasPrefix(req.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "API_ROUTE_NOT_FOUND", "API route not found; verify that the UI and aexp backend versions match")
			return
		}
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

func uiV2CacheControl(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "index.html" {
		return "no-store"
	}
	base := filepath.Base(name)
	if strings.HasPrefix(name, "assets/") && base != "index.js" && base != "index.css" {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
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
	if parseBoolQuery(r.URL.Query().Get("refresh")) {
		runs, _, _ = s.executor.RefreshRuns(ctx, runs, 2*time.Second)
	}

	running := 0
	for _, run := range runs {
		if store.IsRunActiveLifecycleStatus(run.Status) {
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

func (s *Server) handleListStorageTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.ListStorageTargets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_STORAGE_TARGETS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":           targets,
		"control_plane":   "aexp",
		"local_data_path": false,
	})
}

func (s *Server) handleSaveStorageTarget(w http.ResponseWriter, r *http.Request) {
	var target store.StorageTarget
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	target.Name = strings.TrimSpace(target.Name)
	target.RootPath = strings.TrimSpace(target.RootPath)
	if target.Name == "" || target.ResourceID == "" || target.RootPath == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "name, resource_id, root_path are required")
		return
	}
	if !strings.HasPrefix(target.RootPath, "/") {
		writeError(w, http.StatusBadRequest, "INVALID_ROOT", "root_path must be absolute on the NAS")
		return
	}
	if target.ID == "" {
		target.ID = genID("storage_")
	}
	if target.Kind == "" {
		target.Kind = store.StorageKindSSHRsync
	}
	if err := s.store.SaveStorageTarget(r.Context(), &target); err != nil {
		writeError(w, http.StatusConflict, "SAVE_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, target)
}

func (s *Server) handleGetStorageTarget(w http.ResponseWriter, r *http.Request) {
	target, err := s.store.GetStorageTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "STORAGE_TARGET_NOT_FOUND", "storage target not found")
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func (s *Server) handleUpdateStorageTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetStorageTarget(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "STORAGE_TARGET_NOT_FOUND", "storage target not found")
		return
	}
	var update store.StorageTarget
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	update.Name = strings.TrimSpace(update.Name)
	update.RootPath = strings.TrimSpace(update.RootPath)
	if update.Name == "" || update.ResourceID == "" || update.RootPath == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "name, resource_id, root_path are required")
		return
	}
	if !strings.HasPrefix(update.RootPath, "/") {
		writeError(w, http.StatusBadRequest, "INVALID_ROOT", "root_path must be absolute on the NAS")
		return
	}
	update.ID = existing.ID
	update.Kind = existing.Kind
	update.ConfigJSON = existing.ConfigJSON
	// Connection or root metadata may have changed through the backing resource.
	// Never keep a previously green readiness result across an edit.
	update.Status = store.StorageStatusUnknown
	update.LastError = "configuration changed; run a new readiness check"
	update.CreatedAt = existing.CreatedAt
	if err := s.store.SaveStorageTarget(r.Context(), &update); err != nil {
		writeError(w, http.StatusConflict, "UPDATE_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, update)
}

func (s *Server) handleDeleteStorageTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := s.store.GetStorageTarget(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "STORAGE_TARGET_NOT_FOUND", "storage target not found")
		return
	}
	usage, err := s.store.GetStorageTargetUsage(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_TARGET_USAGE_FAILED", err.Error())
		return
	}
	if usage.DatasetVersions > 0 || usage.RunFreezes > 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "STORAGE_TARGET_IN_USE", "details": fmt.Sprintf("cannot delete: referenced by %d dataset version(s) and %d run freeze(s)", usage.DatasetVersions, usage.RunFreezes), "usage": usage,
		})
		return
	}
	if err := s.store.DeleteStorageTarget(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": id, "nas_data_deleted": false, "resource_deleted": false,
	})
}

func (s *Server) handleTestStorageTarget(w http.ResponseWriter, r *http.Request) {
	target, err := s.store.GetStorageTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_STORAGE_TARGET_FAILED", err.Error())
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "STORAGE_TARGET_NOT_FOUND", "storage target not found")
		return
	}
	resource, err := s.store.GetResource(r.Context(), target.ResourceID)
	if err != nil || resource == nil {
		writeError(w, http.StatusConflict, "STORAGE_RESOURCE_MISSING", fmt.Sprintf("storage resource %s not found", target.ResourceID))
		return
	}
	if s.executor == nil || s.executor.Pool() == nil {
		writeError(w, http.StatusServiceUnavailable, "EXECUTOR_UNAVAILABLE", "storage checks require the SSH executor")
		return
	}

	root := apiShellQuote(target.RootPath)
	command := fmt.Sprintf(`printf 'hostname\t%%s\n' "$(hostname 2>/dev/null || true)"
if command -v rsync >/dev/null 2>&1; then printf 'rsync\tok\n'; else printf 'rsync\tmissing\n'; fi
if test -d %s; then printf 'root_exists\tok\n'; else printf 'root_exists\tmissing\n'; fi
if test -r %s; then printf 'root_read\tok\n'; else printf 'root_read\tdenied\n'; fi
if test -w %s; then printf 'root_write\tok\n'; else printf 'root_write\tdenied\n'; fi
df -Pk %s 2>/dev/null | awk 'NR==2 { gsub(/%%/, "", $5); printf "df\t%%s\t%%s\t%%s\t%%s\t%%s\n", $1, $2, $3, $4, $5 }'
exit 0`, root, root, root, root)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	started := time.Now()
	stdout, stderr, checkErr := s.executor.Pool().Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, command, resource.SocksProxy, resource.ProxyCommand)
	health := parseStorageHealth(stdout, strings.TrimSpace(stderr), checkErr, time.Since(started))
	if health.ControlPlane == store.StorageStatusHealthy {
		health.DataPlane = s.probeStorageDataPlane(r.Context(), resource, target)
		ready := 0
		for _, edge := range health.DataPlane {
			if edge.Status == store.StorageStatusHealthy {
				ready++
			}
		}
		health.Usable = ready > 0
		if ready < len(health.DataPlane) {
			// Edge availability is a separate data-plane fact. A failed compute
			// route must not turn a healthy NAS/root/capacity check into a NAS
			// failure; clients render each edge independently.
			if ready == 0 && len(health.DataPlane) == 0 {
				health.Error = "control plane is healthy, but no GPU compute resource is registered for a data-plane check"
			} else if ready == 0 {
				health.Error = fmt.Sprintf("control plane is healthy, but 0/%d compute data paths can transfer with the NAS in either direction", len(health.DataPlane))
			} else {
				health.Error = fmt.Sprintf("control plane is healthy and %d/%d compute data paths can transfer with the NAS", ready, len(health.DataPlane))
			}
		}
	}
	target.Health = health
	target.Status = health.ControlPlane
	target.LastCheckedAt = &health.CheckedAt
	if health.ControlPlane == store.StorageStatusHealthy {
		target.LastError = ""
	} else {
		target.LastError = health.Error
	}
	if err := s.store.SaveStorageTarget(r.Context(), target); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_STORAGE_HEALTH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func parseStorageHealth(stdout, stderr string, checkErr error, elapsed time.Duration) *store.StorageTargetHealth {
	health := &store.StorageTargetHealth{
		Status: store.StorageStatusUnreachable, LatencyMS: elapsed.Milliseconds(), CheckedAt: time.Now(), Checks: map[string]store.StorageHealthCheck{}, DataPlane: []store.StorageDataPlaneHealth{},
	}
	if checkErr != nil {
		health.Checks["ssh"] = store.StorageHealthCheck{OK: false, Detail: checkErr.Error()}
		health.Error = checkErr.Error()
		if stderr != "" {
			health.Error += ": " + stderr
		}
		return health
	}
	health.ControlPlane = store.StorageStatusHealthy
	health.Checks["ssh"] = store.StorageHealthCheck{OK: true, Detail: fmt.Sprintf("connected in %d ms", health.LatencyMS)}
	for _, line := range strings.Split(stdout, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "hostname":
			health.Hostname = parts[1]
		case "rsync", "root_exists", "root_read", "root_write":
			health.Checks[parts[0]] = store.StorageHealthCheck{OK: parts[1] == "ok", Detail: parts[1]}
		case "df":
			if len(parts) >= 6 {
				health.Filesystem = parts[1]
				health.TotalBytes = parseInt64Default(parts[2]) * 1024
				health.UsedBytes = parseInt64Default(parts[3]) * 1024
				health.AvailableBytes = parseInt64Default(parts[4]) * 1024
				health.UsedPercent = int(parseInt64Default(parts[5]))
				health.Checks["capacity"] = store.StorageHealthCheck{OK: health.TotalBytes > 0, Detail: fmt.Sprintf("%d%% used", health.UsedPercent)}
			}
		}
	}
	missing := make([]string, 0)
	for _, name := range []string{"rsync", "root_exists", "root_read", "root_write", "capacity"} {
		check, ok := health.Checks[name]
		if !ok || !check.OK {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		health.Status = store.StorageStatusHealthy
	} else {
		health.ControlPlane = store.StorageStatusDegraded
		health.Status = store.StorageStatusDegraded
		health.Error = "failed checks: " + strings.Join(missing, ", ")
		if stderr != "" {
			health.Error += ": " + stderr
		}
	}
	return health
}

func (s *Server) probeStorageDataPlane(ctx context.Context, nas *store.Resource, target *store.StorageTarget) []store.StorageDataPlaneHealth {
	resources, err := s.store.ListResources(ctx)
	if err != nil {
		return nil
	}
	candidates := make([]store.Resource, 0)
	for _, resource := range resources {
		if resource.ID != nas.ID && (strings.TrimSpace(resource.GPUIndices) != "" || strings.EqualFold(resource.OSType, "linux")) {
			candidates = append(candidates, resource)
		}
	}
	results := make([]store.StorageDataPlaneHealth, len(candidates))
	var wg sync.WaitGroup
	for index := range candidates {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			compute := candidates[index]
			result := store.StorageDataPlaneHealth{ResourceID: compute.ID, ResourceName: compute.Name, Status: store.StorageStatusUnreachable, CheckedAt: time.Now()}

			nasEndpoint := fmt.Sprintf("%s@%s", nas.User, nas.Host)
			inner := fmt.Sprintf("command -v rsync >/dev/null 2>&1 && test -r %s && test -w %s", apiShellQuote(target.RootPath), apiShellQuote(target.RootPath))
			command := fmt.Sprintf("command -v rsync >/dev/null 2>&1 && printf 'compute_rsync\\tok\\n' || printf 'compute_rsync\\tmissing\\n'; ssh -p %d -o BatchMode=yes -o ConnectTimeout=6 -o StrictHostKeyChecking=accept-new %s %s >/dev/null 2>&1 && printf 'nas_edge\\tok\\n' || printf 'nas_edge\\tfailed\\n'; exit 0", nas.Port, apiShellQuote(nasEndpoint), apiShellQuote(inner))
			probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			started := time.Now()
			stdout, stderr, execErr := s.executor.Pool().Exec(probeCtx, compute.Host, compute.Port, compute.User, compute.AuthRef, executor.WithResourceRemotePath(&compute, command), compute.SocksProxy, compute.ProxyCommand)
			cancel()
			computePath := store.StorageConnectionHealth{Status: store.StorageStatusUnreachable, LatencyMS: time.Since(started).Milliseconds()}
			if execErr != nil {
				computePath.Error = execErr.Error()
				if strings.TrimSpace(stderr) != "" {
					computePath.Error += ": " + strings.TrimSpace(stderr)
				}
			} else {
				computePath.Rsync = strings.Contains(stdout, "compute_rsync\tok")
				computePath.SSHReachable = strings.Contains(stdout, "nas_edge\tok")
				if computePath.Rsync && computePath.SSHReachable {
					computePath.Status = store.StorageStatusHealthy
				} else {
					failures := make([]string, 0, 2)
					if !computePath.Rsync {
						failures = append(failures, "rsync missing on compute")
					}
					if !computePath.SSHReachable {
						failures = append(failures, "compute cannot SSH/read/write NAS root")
					}
					computePath.Error = strings.Join(failures, "; ")
				}
			}

			computeEndpoint := fmt.Sprintf("%s@%s", compute.User, compute.Host)
			computeRoot := strings.TrimSpace(compute.RootDir)
			if computeRoot == "" {
				computeRoot = "."
			}
			computeInner := fmt.Sprintf("command -v rsync >/dev/null 2>&1 && test -r %s && test -w %s", apiShellQuote(computeRoot), apiShellQuote(computeRoot))
			nasCommand := fmt.Sprintf("identity=\"$HOME/%s\"; command -v rsync >/dev/null 2>&1 && test -r \"$identity\" && printf 'nas_rsync\\tok\\n' || printf 'nas_rsync\\tmissing\\n'; ssh -i \"$identity\" -p %d -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=6 -o StrictHostKeyChecking=accept-new %s %s >/dev/null 2>&1 && printf 'compute_edge\\tok\\n' || printf 'compute_edge\\tfailed\\n'; exit 0", store.NASInitiatedIdentity, compute.Port, apiShellQuote(computeEndpoint), apiShellQuote(computeInner))
			reverseCtx, reverseCancel := context.WithTimeout(ctx, 12*time.Second)
			reverseStarted := time.Now()
			reverseOut, reverseErrOut, reverseErr := s.executor.Pool().Exec(reverseCtx, nas.Host, nas.Port, nas.User, nas.AuthRef, executor.WithResourceRemotePath(nas, nasCommand), nas.SocksProxy, nas.ProxyCommand)
			reverseCancel()
			nasPath := store.StorageConnectionHealth{Status: store.StorageStatusUnreachable, LatencyMS: time.Since(reverseStarted).Milliseconds()}
			if reverseErr != nil {
				nasPath.Error = reverseErr.Error()
				if strings.TrimSpace(reverseErrOut) != "" {
					nasPath.Error += ": " + strings.TrimSpace(reverseErrOut)
				}
			} else {
				nasPath.Rsync = strings.Contains(reverseOut, "nas_rsync\tok")
				nasPath.SSHReachable = strings.Contains(reverseOut, "compute_edge\tok")
				if nasPath.Rsync && nasPath.SSHReachable {
					nasPath.Status = store.StorageStatusHealthy
				} else {
					failures := make([]string, 0, 2)
					if !nasPath.Rsync {
						failures = append(failures, "rsync or dedicated identity missing on NAS")
					}
					if !nasPath.SSHReachable {
						failures = append(failures, "NAS cannot SSH/read/write compute root")
					}
					nasPath.Error = strings.Join(failures, "; ")
				}
			}

			result.ComputeInitiated = computePath
			result.NASInitiated = nasPath
			selected := computePath
			result.SelectedInitiator = store.StorageInitiatorCompute
			if nasPath.Status == store.StorageStatusHealthy {
				selected = nasPath
				result.SelectedInitiator = store.StorageInitiatorNAS
			}
			result.Status, result.LatencyMS = selected.Status, selected.LatencyMS
			result.Rsync, result.NASReachable = selected.Rsync, selected.SSHReachable
			result.Error = selected.Error
			results[index] = result
		}()
	}
	wg.Wait()
	return results
}

func parseInt64Default(value string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return n
}

func apiShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (s *Server) handleListDatasetVersions(w http.ResponseWriter, r *http.Request) {
	datasets, err := s.store.ListDatasetVersions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_DATASETS_FAILED", err.Error())
		return
	}
	type datasetWithMaterializations struct {
		store.DatasetVersion
		Materializations []store.DatasetMaterialization `json:"materializations"`
	}
	items := make([]datasetWithMaterializations, 0, len(datasets))
	for _, dataset := range datasets {
		materializations, _ := s.store.ListDatasetMaterializations(r.Context(), dataset.ID)
		items = append(items, datasetWithMaterializations{DatasetVersion: dataset, Materializations: materializations})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "local_data_path": false})
}

func (s *Server) handleListDatasetMaterializations(w http.ResponseWriter, r *http.Request) {
	datasetID := chi.URLParam(r, "id")
	if dataset, err := s.store.GetDatasetVersion(r.Context(), datasetID); err != nil {
		writeError(w, http.StatusInternalServerError, "GET_DATASET_FAILED", err.Error())
		return
	} else if dataset == nil {
		writeError(w, http.StatusNotFound, "DATASET_NOT_FOUND", "dataset version not found")
		return
	}
	items, err := s.store.ListDatasetMaterializations(r.Context(), datasetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_MATERIALIZATIONS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "local_data_path": false})
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
	now := time.Now()
	res.SSHStatus = store.ResourceSSHStatusOK
	res.LastCheckedAt = &now
	res.LastSuccessAt = &now

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
	osType := localOSType()
	return store.Resource{
		Name:       name,
		Type:       store.ResourceTypeSSH,
		Host:       "127.0.0.1",
		OSType:     osType,
		Port:       22,
		User:       userName,
		AuthRef:    keyPath,
		RootDir:    rootDir,
		RemotePath: executor.EffectiveRemotePath(&store.Resource{OSType: osType}),
		CondaBase:  condaBase,
		CondaInit:  condaInit,
		Status:     store.ResourceStatusUnknown,
		Tags:       "local",
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
	if !hasField("remote_path") {
		update.RemotePath = existing.RemotePath
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
	if !hasField("ssh_status") {
		update.SSHStatus = existing.SSHStatus
	}
	if !hasField("last_doctor_error") {
		update.LastDoctorError = existing.LastDoctorError
	}
	if !hasField("last_checked_at") {
		update.LastCheckedAt = existing.LastCheckedAt
	}
	if !hasField("last_success_at") {
		update.LastSuccessAt = existing.LastSuccessAt
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

func (s *Server) handleRefreshResource(w http.ResponseWriter, r *http.Request) {
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
	if s.monitor == nil {
		writeError(w, http.StatusServiceUnavailable, "MONITOR_UNAVAILABLE", "resource monitor is not running")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	refreshed, snap, refreshErr := s.monitor.RefreshResource(ctx, res)
	if refreshed == nil {
		refreshed = res
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":              refreshErr == nil,
		"resource":        refreshed,
		"latest_snapshot": snap,
		"error":           errorString(refreshErr),
	})
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
		ResourceID:     r.URL.Query().Get("resource"),
		ProjectID:      r.URL.Query().Get("project"),
		ProjectScopeID: r.URL.Query().Get("project_scope"),
		Status:         r.URL.Query().Get("status"),
		Query:          r.URL.Query().Get("query"),
		KindGroup:      r.URL.Query().Get("kind_group"),
		Trash:          parseBoolQuery(r.URL.Query().Get("trash")),
		Deleted:        parseBoolQuery(r.URL.Query().Get("deleted")),
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
	if parseBoolQuery(r.URL.Query().Get("refresh")) {
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
	if parseBoolQuery(r.URL.Query().Get("meta")) {
		total, err := s.store.CountRuns(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, paginatedResponse[store.Run]{
			Items:  runs,
			Total:  total,
			Limit:  filter.Limit,
			Offset: filter.Offset,
		})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleListActiveRunSummaries(w http.ResponseWriter, r *http.Request) {
	filter := store.RunFilter{Active: true}
	filter.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	changeCursor, err := s.store.LatestRunChangeSeq(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	summaries, err := s.store.ListRunSummaries(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	total, err := s.store.CountRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runSummaryPage{Items: summaries, Total: total, Limit: filter.Limit, ChangeCursor: changeCursor})
}

func runFilterFromRequest(r *http.Request) store.RunFilter {
	filter := store.RunFilter{
		ResourceID:     r.URL.Query().Get("resource"),
		ProjectID:      r.URL.Query().Get("project"),
		ProjectScopeID: r.URL.Query().Get("project_scope"),
		Status:         r.URL.Query().Get("status"),
		Query:          r.URL.Query().Get("query"),
		KindGroup:      r.URL.Query().Get("kind_group"),
		Trash:          parseBoolQuery(r.URL.Query().Get("trash")),
		Deleted:        parseBoolQuery(r.URL.Query().Get("deleted")),
	}
	filter.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	filter.Offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	return filter
}

func (s *Server) handleListRunSummaries(w http.ResponseWriter, r *http.Request) {
	filter := runFilterFromRequest(r)
	changeCursor, err := s.store.LatestRunChangeSeq(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	summaries, err := s.store.ListRunSummaries(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	total, err := s.store.CountRuns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runSummaryPage{Items: summaries, Total: total, Limit: filter.Limit, Offset: filter.Offset, ChangeCursor: changeCursor})
}

type runChangeItem struct {
	store.RunChange
	Run *store.RunSummary `json:"run,omitempty"`
}

type runChangeResponse struct {
	Items      []runChangeItem `json:"items"`
	NextSeq    int64           `json:"next_seq"`
	ServerTime time.Time       `json:"server_time"`
}

func (s *Server) handleListRunChanges(w http.ResponseWriter, r *http.Request) {
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var updatedSince *time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("updated_since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_UPDATED_SINCE", "updated_since must be RFC3339")
			return
		}
		updatedSince = &parsed
	}
	changes, err := s.store.ListRunChanges(r.Context(), afterSeq, updatedSince, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	response := runChangeResponse{Items: make([]runChangeItem, 0, len(changes)), NextSeq: afterSeq, ServerTime: time.Now().UTC().Truncate(time.Millisecond)}
	for _, change := range changes {
		item := runChangeItem{RunChange: change}
		if change.Operation != store.RunChangeDelete {
			item.Run, err = s.store.GetRunSummary(r.Context(), change.RunID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
				return
			}
		}
		response.Items = append(response.Items, item)
		response.NextSeq = change.Seq
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleRunChangeStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "streaming is unavailable")
		return
	}
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	if afterSeq == 0 {
		if headerSeq, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Last-Event-ID")), 10, 64); err == nil {
			afterSeq = headerSeq
		}
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// Commit the stream response immediately. Clients must be able to finish
	// connecting before the next change or heartbeat exists.
	flusher.Flush()

	poll := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()

	writeChanges := func() error {
		changes, err := s.store.ListRunChanges(r.Context(), afterSeq, nil, 200)
		if err != nil {
			return err
		}
		for _, change := range changes {
			item := runChangeItem{RunChange: change}
			if change.Operation != store.RunChangeDelete {
				item.Run, err = s.store.GetRunSummary(r.Context(), change.RunID)
				if err != nil {
					return err
				}
			}
			payload, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: run-change\ndata: %s\n\n", change.Seq, payload); err != nil {
				return err
			}
			afterSeq = change.Seq
		}
		if len(changes) > 0 {
			flusher.Flush()
		}
		return nil
	}

	if err := writeChanges(); err != nil {
		s.logger.Warn("run change stream initial replay failed", "error", err)
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			if err := writeChanges(); err != nil {
				s.logger.Warn("run change stream poll failed", "error", err)
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleArchiveRun(w http.ResponseWriter, r *http.Request) {
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
	if store.IsRunActiveLifecycleStatus(run.Status) {
		writeError(w, http.StatusBadRequest, "RUN_ACTIVE", "running runs cannot be moved to trash")
		return
	}
	if err := s.store.ArchiveRun(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	run, _ = s.store.GetRun(r.Context(), id)
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRestoreRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.RestoreRun(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
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

func (s *Server) handleDeleteRunLogically(w http.ResponseWriter, r *http.Request) {
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
	if store.IsRunActiveLifecycleStatus(run.Status) {
		writeError(w, http.StatusBadRequest, "RUN_ACTIVE", "running runs cannot be deleted")
		return
	}
	if !run.ArchivedAt.Valid && !run.DeletedAt.Valid {
		writeError(w, http.StatusBadRequest, "RUN_NOT_ARCHIVED", "move run to trash before deleting it")
		return
	}
	if err := s.store.DeleteRunLogically(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	var req executor.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	run, err := s.executor.SubmitAsync(r.Context(), req, executor.SubmitOptions{})
	if err != nil {
		writeError(w, http.StatusBadRequest, "SUBMIT_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, run)
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
	if parseBoolQuery(r.URL.Query().Get("refresh")) && store.IsRunRefreshableStatus(run.Status) {
		if refreshed, err := s.executor.CheckRunStatus(r.Context(), id); err == nil && refreshed != nil {
			run = refreshed
		}
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleAssignRunProject(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ProjectID         string  `json:"project_id"`
		ExpectedProjectID *string `json:"expected_project_id"`
		Actor             string  `json:"actor"`
		Reason            string  `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if request.ExpectedProjectID == nil {
		writeError(w, http.StatusBadRequest, "EXPECTED_PROJECT_ID_REQUIRED", "expected_project_id is required for conflict-safe Project assignment")
		return
	}
	if strings.TrimSpace(request.Actor) == "" {
		request.Actor = "api"
	}
	result, err := s.store.AssignRunProject(
		r.Context(),
		chi.URLParam(r, "id"),
		request.ProjectID,
		*request.ExpectedProjectID,
		request.Actor,
		request.Reason,
	)
	if err != nil {
		var conflict *store.RunProjectAssignmentConflict
		if errors.As(err, &conflict) {
			writeError(w, http.StatusConflict, "RUN_PROJECT_CONFLICT", conflict.Error())
			return
		}
		var validation *store.EvidenceGraphValidationError
		if errors.As(err, &validation) {
			status := http.StatusBadRequest
			switch validation.Code {
			case "RUN_NOT_FOUND", "PROJECT_NOT_FOUND":
				status = http.StatusNotFound
			case "RUN_ACTIVE":
				status = http.StatusConflict
			}
			writeError(w, status, validation.Code, validation.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetRunDataBindings(w http.ResponseWriter, r *http.Request) {
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
	inputs, err := s.store.ListRunInputBindings(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	outputs, err := s.store.ListRunOutputBindings(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, store.RunBindings{Inputs: inputs, Outputs: outputs})
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
	logPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if source == "" {
		source = "stdout"
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	afterRaw, hasAfter := r.URL.Query()["after_line"]
	afterLine := 0
	if hasAfter && len(afterRaw) > 0 {
		afterLine, _ = strconv.Atoi(afterRaw[0])
	}
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
	var logError string
	var logErrorKind string
	reset := false
	cursorRemote := false
	tailMode := parseBoolQuery(r.URL.Query().Get("tail"))
	if hasAfter && s.executor != nil {
		remoteCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		var remoteLines []executor.LogLine
		var err error
		if logPath != "" {
			remoteLines, total, reset, err = s.executor.GetLogFileSnapshotAfter(remoteCtx, id, logPath, afterLine, limit)
			source = logPath
		} else {
			remoteLines, total, reset, err = s.executor.GetLogSnapshotAfter(remoteCtx, id, source, afterLine, limit)
		}
		cancel()
		if err == nil {
			lines = executorLogLinesToStore(remoteLines)
			remote, cursorRemote = true, true
		} else {
			logError, logErrorKind = err.Error(), logReadErrorKind(err)
		}
	}
	if !cursorRemote && logPath != "" {
		var err error
		lines, total, remote, err = s.remoteLogFileLines(r.Context(), id, logPath, limit)
		if err != nil && len(lines) == 0 {
			logError = err.Error()
			logErrorKind = logReadErrorKind(err)
		}
		source = logPath
	} else if !cursorRemote && tailMode && offset == 0 {
		lines, total, remote = s.remoteLogLines(r.Context(), id, source, limit)
	}
	if logPath == "" && !remote {
		lines, total, remote = s.fastLogLines(r.Context(), id, source, offset, limit)
	}
	if hasAfter && !cursorRemote {
		if total < afterLine {
			reset = true
		} else {
			filtered := lines[:0]
			for _, line := range lines {
				if line.LineNo > afterLine {
					filtered = append(filtered, line)
				}
			}
			lines = filtered
		}
	}
	firstLine, lastLine := logLineBounds(lines)
	nextCursor := afterLine
	if lastLine > nextCursor {
		nextCursor = lastLine
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id":      id,
		"source":      source,
		"path":        logPath,
		"total_lines": total,
		"offset":      offset,
		"limit":       limit,
		"lines":       lines,
		"remote":      remote,
		"tail":        tailMode,
		"first_line":  firstLine,
		"last_line":   lastLine,
		"next_cursor": nextCursor,
		"reset":       reset,
		"truncated":   firstLine > 1 && total > len(lines),
		"error":       logError,
		"error_kind":  logErrorKind,
	})
}

func (s *Server) remoteLogFileLines(ctx context.Context, runID string, logPath string, limit int) ([]store.LogLine, int, bool, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		if err != nil {
			return nil, 0, false, err
		}
		return nil, 0, false, fmt.Errorf("run %s not found", runID)
	}
	resource, err := s.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		if err != nil {
			return nil, 0, false, err
		}
		return nil, 0, false, fmt.Errorf("resource not found")
	}
	isEventLog := isRunUIEventLog(run, logPath)
	if isEventLog && store.IsRunTerminalStatus(run.Status) {
		if cached, total, ok := cachedEventLogLines(runID, logPath, limit); ok {
			return cached, total, false, nil
		}
	}
	if resource.Status == store.ResourceStatusUnreachable {
		err := fmt.Errorf("resource %s is unreachable; cannot read remote log file %s", resource.Name, logPath)
		if isEventLog {
			if cached, total, ok := cachedEventLogLines(runID, logPath, limit); ok {
				return cached, total, false, err
			}
		}
		return nil, 0, false, err
	}
	if s.executor == nil {
		err := fmt.Errorf("remote executor unavailable; cannot read remote log file %s", logPath)
		if isEventLog {
			if cached, total, ok := cachedEventLogLines(runID, logPath, limit); ok {
				return cached, total, false, err
			}
		}
		return nil, 0, false, err
	}

	remoteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	remoteLines, err := s.executor.GetLogFileSnapshot(remoteCtx, runID, logPath, limit)
	if err != nil {
		if isEventLog {
			if cached, total, ok := cachedEventLogLines(runID, logPath, limit); ok {
				return cached, total, false, err
			}
		}
		return nil, 0, false, err
	}
	lines := make([]store.LogLine, 0, len(remoteLines))
	for _, line := range remoteLines {
		lines = append(lines, store.LogLine{
			RunID:   runID,
			Source:  line.Source,
			LineNo:  line.LineNo,
			Content: line.Content,
		})
	}
	if isEventLog {
		if _, err := eventcache.WriteSnapshot(runID, eventCacheLinesFromExecutor(remoteLines)); err != nil && s.logger != nil {
			s.logger.Warn("cache UI event log", "run_id", runID, "error", err)
		}
	}
	total := 0
	if len(lines) > 0 {
		total = lines[len(lines)-1].LineNo
	}
	return lines, total, true, nil
}

func isRunUIEventLog(run *store.Run, logPath string) bool {
	return run != nil && strings.TrimSpace(run.UIEventsPath) != "" && strings.TrimSpace(run.UIEventsPath) == strings.TrimSpace(logPath)
}

func cachedEventLogLines(runID, source string, limit int) ([]store.LogLine, int, bool) {
	cacheLines, _, err := eventcache.Read(runID, limit)
	if err != nil || len(cacheLines) == 0 {
		return nil, 0, false
	}
	lines := make([]store.LogLine, 0, len(cacheLines))
	for _, line := range cacheLines {
		lines = append(lines, store.LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  line.LineNo,
			Content: line.Content,
		})
	}
	total := lines[len(lines)-1].LineNo
	return lines, total, true
}

func eventCacheLinesFromExecutor(lines []executor.LogLine) []eventcache.Line {
	out := make([]eventcache.Line, 0, len(lines))
	for _, line := range lines {
		out = append(out, eventcache.Line{LineNo: line.LineNo, Content: line.Content})
	}
	return out
}

func executorLogLinesToStore(lines []executor.LogLine) []store.LogLine {
	out := make([]store.LogLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, store.LogLine{RunID: line.RunID, Source: line.Source, LineNo: line.LineNo, Content: line.Content})
	}
	return out
}

func logReadErrorKind(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		return "file_missing"
	case strings.Contains(msg, "unreachable"):
		return "resource_unreachable"
	case strings.Contains(msg, "deadline") || strings.Contains(msg, "timeout") || strings.Contains(msg, "i/o timeout"):
		return "remote_timeout"
	default:
		return "read_failed"
	}
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

func parseIntQuery(v string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return n
	}
	return def
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

func (s *Server) handleGetArtifactCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	collection, err := s.store.GetArtifactCollection(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if collection == nil {
		if run, _ := s.store.GetRun(r.Context(), id); run == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
			return
		}
		collection = &store.ArtifactCollection{RunID: id, State: store.ArtifactCollectionDeclared}
	}
	writeJSON(w, http.StatusOK, collection)
}

func (s *Server) handleCollectArtifacts(w http.ResponseWriter, r *http.Request) {
	if s.executor == nil {
		writeError(w, http.StatusServiceUnavailable, "EXECUTOR_UNAVAILABLE", "executor is unavailable")
		return
	}
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
	if existing, _ := s.store.GetArtifactCollection(r.Context(), id); existing != nil && existing.State == store.ArtifactCollectionDiscovering {
		writeError(w, http.StatusConflict, "COLLECTION_ACTIVE", "artifact collection is already running")
		return
	}
	now := time.Now()
	collection := &store.ArtifactCollection{RunID: id, State: store.ArtifactCollectionDiscovering, StartedAt: &now}
	if err := s.store.SaveArtifactCollection(r.Context(), collection); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = s.executor.CollectArtifacts(ctx, id)
	}()
	writeJSON(w, http.StatusAccepted, collection)
}

func (s *Server) handleGetRunManifest(w http.ResponseWriter, r *http.Request) {
	manifest, err := s.store.GetRunManifest(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if manifest == nil {
		writeError(w, http.StatusNotFound, "MANIFEST_UNAVAILABLE", "this legacy run has no captured manifest")
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleCreateEvidenceSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, created, err := s.store.CreateEvidenceSnapshot(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		var blocked *store.EvidenceSnapshotBlockedError
		if errors.As(err, &blocked) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
				"error":    "SNAPSHOT_BLOCKED",
				"details":  blocked.Error(),
				"blockers": blocked.Blockers,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "SNAPSHOT_CREATE_FAILED", err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, snapshot)
}

func (s *Server) handleListEvidenceSnapshots(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListEvidenceSnapshots(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SNAPSHOT_LIST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetEvidenceSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.store.GetEvidenceSnapshot(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SNAPSHOT_GET_FAILED", err.Error())
		return
	}
	if snapshot == nil {
		writeError(w, http.StatusNotFound, "SNAPSHOT_NOT_FOUND", "evidence snapshot not found")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleCreateEvidenceRelease(w http.ResponseWriter, r *http.Request) {
	release, err := (releaseservice.Service{Store: s.store}).Evaluate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		var blocked *releaseservice.BlockedError
		if errors.As(err, &blocked) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
				"error": "RELEASE_BLOCKED", "code": blocked.Code, "details": blocked.Message,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "RELEASE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, release)
}

func (s *Server) handleListEvidenceReleases(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListEvidenceReleases(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RELEASE_LIST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetEvidenceRelease(w http.ResponseWriter, r *http.Request) {
	release, err := s.store.GetEvidenceRelease(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RELEASE_GET_FAILED", err.Error())
		return
	}
	if release == nil {
		writeError(w, http.StatusNotFound, "RELEASE_NOT_FOUND", "evidence release not found")
		return
	}
	writeJSON(w, http.StatusOK, release)
}

type runFreezeRequest struct {
	Profile          string `json:"profile"`
	To               string `json:"to"`
	Workspace        string `json:"workspace"`
	ProjectConfig    string `json:"project_config"`
	ExpectedPlanHash string `json:"expected_plan_hash"`
}

func runFreezeCLI(ctx context.Context, runID string, req runFreezeRequest, execute bool) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"run", "freeze", runID, "--profile", firstNonEmpty(req.Profile, "paper"), "--json"}
	if !execute {
		args = append(args, "--dry-run")
	}
	if req.To != "" {
		args = append(args, "--to", req.To)
	}
	if req.Workspace != "" {
		args = append(args, "--workspace", req.Workspace)
	}
	if req.ProjectConfig != "" {
		args = append(args, "--config", req.ProjectConfig)
	}
	cmd := osexec.CommandContext(ctx, exe, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func decodeRunFreezeRequest(r *http.Request) (runFreezeRequest, error) {
	var req runFreezeRequest
	if r.Body != nil {
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil && err != io.EOF {
			return req, err
		}
	}
	if req.Profile == "" {
		req.Profile = "paper"
	}
	return req, nil
}

func (s *Server) handlePlanRunFreeze(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRunFreezeRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	out, err := runFreezeCLI(r.Context(), chi.URLParam(r, "id"), req, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FREEZE_PLAN_FAILED", err.Error())
		return
	}
	var payload interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		writeError(w, http.StatusInternalServerError, "FREEZE_PLAN_DECODE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleCreateRunFreeze(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRunFreezeRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	planRaw, err := runFreezeCLI(r.Context(), chi.URLParam(r, "id"), req, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FREEZE_PLAN_FAILED", err.Error())
		return
	}
	var plan struct {
		PlanSHA256 string `json:"plan_sha256"`
		Eligible   bool   `json:"eligible"`
	}
	if err := json.Unmarshal(planRaw, &plan); err != nil {
		writeError(w, http.StatusInternalServerError, "FREEZE_PLAN_DECODE_FAILED", err.Error())
		return
	}
	if req.ExpectedPlanHash == "" || req.ExpectedPlanHash != plan.PlanSHA256 {
		writeError(w, http.StatusConflict, "STALE_FREEZE_PLAN", "expected_plan_hash does not match the current plan")
		return
	}
	if !plan.Eligible {
		writeError(w, http.StatusConflict, "FREEZE_BLOCKED", "freeze plan has blockers")
		return
	}
	out, err := runFreezeCLI(r.Context(), chi.URLParam(r, "id"), req, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FREEZE_CREATE_FAILED", err.Error())
		return
	}
	var payload interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		writeError(w, http.StatusInternalServerError, "FREEZE_CREATE_DECODE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, payload)
}

func (s *Server) handleListRunFreezes(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRunFreezes(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FREEZES_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}
func (s *Server) handleGetRunFreeze(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRunFreeze(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_FREEZE_FAILED", err.Error())
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "FREEZE_NOT_FOUND", "freeze not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) handleGetRunFreezeManifest(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRunFreeze(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_FREEZE_FAILED", err.Error())
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "FREEZE_NOT_FOUND", "freeze not found")
		return
	}
	files, err := s.store.ListRunFreezeFiles(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_FREEZE_MANIFEST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"freeze": item, "files": files})
}

func (s *Server) handleStatusCheck(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := s.executor.CheckRunStatus(r.Context(), id)
	if err != nil {
		if run != nil {
			// A failed remote probe does not invalidate the cached lifecycle.
			// The Run carries status_check_error/source/freshness so callers can
			// distinguish stale cached state from a fresh remote observation.
			writeJSON(w, http.StatusOK, run)
			return
		}
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
	mark.Statement = strings.TrimSpace(mark.Statement)
	mark.BodyMD = strings.TrimSpace(mark.BodyMD)
	mark.Reason = strings.TrimSpace(mark.Reason)
	mark.Evidence = strings.TrimSpace(mark.Evidence)
	if mark.Actor == "" {
		mark.Actor = "api"
	}
	if mark.Kind == "" {
		mark.Kind = "key_result"
	}
	if mark.Title == "" && mark.Statement == "" && mark.BodyMD == "" && mark.Reason == "" && mark.Evidence == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELDS", "title, statement, body_md, reason, or evidence is required")
		return
	}

	if err := s.store.SaveRunMark(r.Context(), &mark); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mark)
}

func (s *Server) handleGetRunMarkAttachmentBlob(w http.ResponseWriter, r *http.Request) {
	markID := chi.URLParam(r, "id")
	attachmentID := chi.URLParam(r, "attachmentID")
	attachment, err := s.store.GetRunMarkAttachment(r.Context(), markID, attachmentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if attachment == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run mark attachment not found")
		return
	}
	path := filepath.Clean(attachment.LocalPath)
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "attachment file not found")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "attachment file not readable")
		return
	}
	contentType := attachment.Mime
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(attachment.Filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", attachment.Filename))
	http.ServeContent(w, r, attachment.Filename, stat.ModTime(), file)
}

func runMarkFilterFromQuery(r *http.Request) store.RunMarkFilter {
	filter := store.RunMarkFilter{
		RunID: r.URL.Query().Get("run_id"),
		Actor: r.URL.Query().Get("actor"),
		Kind:  r.URL.Query().Get("kind"),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("run_ids")); raw != "" {
		for _, runID := range strings.Split(raw, ",") {
			if runID = strings.TrimSpace(runID); runID != "" {
				filter.RunIDs = append(filter.RunIDs, runID)
				if len(filter.RunIDs) == 100 {
					break
				}
			}
		}
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

// --- Executable Project Definitions and Targets ---

type projectDefinitionDetail struct {
	store.ProjectDefinition
	Targets []store.ProjectTarget `json:"targets"`
}

func (s *Server) handleListProjectDefinitions(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjectDefinitions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if projects == nil {
		projects = []store.ProjectDefinition{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleGetProjectDefinition(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.GetProjectDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project definition not found")
		return
	}
	targets, err := s.store.ListProjectTargets(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if targets == nil {
		targets = []store.ProjectTarget{}
	}
	writeJSON(w, http.StatusOK, projectDefinitionDetail{ProjectDefinition: *project, Targets: targets})
}

func (s *Server) handleListProjectAssets(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	if project, err := s.store.GetProjectDefinition(r.Context(), projectID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if project == nil {
		writeError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	items, total, err := s.store.ListProjectAssets(r.Context(), projectID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PROJECT_ASSETS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, paginatedResponse[store.ProjectAsset]{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (s *Server) handleSaveProjectDefinition(w http.ResponseWriter, r *http.Request) {
	var project store.ProjectDefinition
	if err := json.NewDecoder(r.Body).Decode(&project); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	pathID := chi.URLParam(r, "id")
	if pathID != "" {
		if project.ID != "" && project.ID != pathID {
			writeError(w, http.StatusBadRequest, "ID_MISMATCH", "project id does not match URL")
			return
		}
		project.ID = pathID
	}
	project.ID = strings.TrimSpace(project.ID)
	project.Name = strings.TrimSpace(project.Name)
	if project.ID == "" {
		project.ID = genID("project_")
	}
	if project.Name == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "project name is required")
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		if err := s.store.CreateProjectDefinition(r.Context(), &project); err != nil {
			writeEvidenceGraphError(w, err)
			return
		}
		status = http.StatusCreated
	} else if err := s.store.SaveProjectDefinition(r.Context(), &project); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, status, project)
}

func (s *Server) handleGetProjectEvidenceMap(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	if project, err := s.store.GetProjectDefinition(r.Context(), projectID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if project == nil {
		writeError(w, http.StatusNotFound, "PROJECT_NOT_REGISTERED", "project definition not found")
		return
	}
	chain, err := s.store.GetActivePrimaryEvidenceChain(r.Context(), projectID)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	if chain == nil {
		writeError(w, http.StatusNotFound, "PRIMARY_MAP_NOT_FOUND", "project has no active primary evidence map")
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

func (s *Server) handleEnsureProjectEvidenceMap(w http.ResponseWriter, r *http.Request) {
	chain, err := s.store.EnsureProjectPrimaryEvidenceChain(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

func (s *Server) handleDeleteProjectDefinition(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteProjectDefinition(r.Context(), id); err != nil {
		var inUse *store.ProjectInUseError
		if errors.As(err, &inUse) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":      "PROJECT_IN_USE",
				"details":    inUse.Error(),
				"references": inUse.Counts,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListProjectTargets(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	if project, err := s.store.GetProjectDefinition(r.Context(), projectID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if project == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project definition not found")
		return
	}
	targets, err := s.store.ListProjectTargets(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if targets == nil {
		targets = []store.ProjectTarget{}
	}
	for i := range targets {
		s.refreshTargetReadiness(r.Context(), &targets[i])
	}
	writeJSON(w, http.StatusOK, targets)
}

func (s *Server) handleGetProjectTarget(w http.ResponseWriter, r *http.Request) {
	target, err := s.store.GetProjectTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project target not found")
		return
	}
	s.refreshTargetReadiness(r.Context(), target)
	writeJSON(w, http.StatusOK, target)
}

func (s *Server) refreshTargetReadiness(ctx context.Context, target *store.ProjectTarget) {
	if target == nil {
		return
	}
	if target.LastPrepareRunID == "" {
		if target.Readiness == store.TargetReadinessChecking && target.ReadinessObservedAt != nil && time.Since(*target.ReadinessObservedAt) > 5*time.Minute {
			now := time.Now()
			target.Readiness = store.TargetReadinessFailed
			target.ReadinessObservedAt = &now
			target.ReadinessError = "prepare reservation expired before a tracked run was created"
			_ = s.store.SaveProjectTarget(ctx, target)
		}
		return
	}
	run, err := s.store.GetRun(ctx, target.LastPrepareRunID)
	if err != nil || run == nil {
		return
	}
	previous := target.Readiness
	now := time.Now()
	switch run.Status {
	case store.RunStatusStarting, store.RunStatusRunning, store.RunStatusSSHUnreachable:
		target.Readiness = store.TargetReadinessChecking
	case store.RunStatusSucceeded:
		target.Readiness = store.TargetReadinessReady
		target.ReadinessError = ""
		target.LastPreparedAt = &now
		if project, _ := s.store.GetProjectDefinition(ctx, target.ProjectID); project != nil {
			target.ObservedConfigHash = project.ConfigHash
		}
	default:
		if store.IsRunTerminalStatus(run.Status) {
			target.Readiness = store.TargetReadinessFailed
			target.ReadinessError = firstNonEmptyString(run.FailureReason, "prepare run ended with status "+run.Status)
		}
	}
	if target.Readiness == store.TargetReadinessReady {
		if project, _ := s.store.GetProjectDefinition(ctx, target.ProjectID); project != nil && project.ConfigHash != "" && target.ObservedConfigHash != project.ConfigHash {
			target.Readiness = store.TargetReadinessDrifted
			target.ReadinessError = "project configuration changed after the last successful prepare"
		}
	}
	if target.Readiness != previous || target.Readiness == store.TargetReadinessReady {
		target.ReadinessObservedAt = &now
		_ = s.store.SaveProjectTarget(ctx, target)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type projectTargetPrepareStage struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Mutates     bool   `json:"mutates"`
}

type projectTargetPreparePlan struct {
	ProjectID     string                      `json:"project_id"`
	TargetID      string                      `json:"target_id"`
	ResourceID    string                      `json:"resource_id"`
	Cwd           string                      `json:"cwd"`
	Command       string                      `json:"command"`
	EvidenceGrade string                      `json:"evidence_grade"`
	Stages        []projectTargetPrepareStage `json:"stages"`
	Warnings      []string                    `json:"warnings"`
}

func (s *Server) loadPrepareTarget(ctx context.Context, projectID, targetID string) (*store.ProjectDefinition, *store.ProjectTarget, error) {
	project, err := s.store.GetProjectDefinition(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	if project == nil {
		return nil, nil, fmt.Errorf("project definition not found")
	}
	target, err := s.store.GetProjectTarget(ctx, targetID)
	if err != nil {
		return nil, nil, err
	}
	if target == nil || target.ProjectID != projectID {
		return nil, nil, fmt.Errorf("project target not found")
	}
	return project, target, nil
}

func buildProjectTargetPreparePlan(project *store.ProjectDefinition, target *store.ProjectTarget) projectTargetPreparePlan {
	warnings := []string{}
	if strings.TrimSpace(target.PrepareCommand) == "" {
		warnings = append(warnings, "no prepare command is configured")
	}
	if strings.TrimSpace(project.ConfigHash) == "" {
		warnings = append(warnings, "project config has no fingerprint; drift detection is limited")
	}
	return projectTargetPreparePlan{
		ProjectID: project.ID, TargetID: target.ID, ResourceID: target.ResourceID, Cwd: target.Cwd,
		Command: target.PrepareCommand, EvidenceGrade: "none", Warnings: warnings,
		Stages: []projectTargetPrepareStage{
			{Name: "inspect", Description: "validate resource, target directory, and desired environment", Mutates: false},
			{Name: "prepare", Description: target.PrepareCommand, Mutates: true},
			{Name: "verify", Description: "require the prepare command to exit successfully", Mutates: false},
			{Name: "finalize", Description: "record target readiness and configuration fingerprint", Mutates: false},
		},
	}
}

func (s *Server) handleProjectTargetPreparePlan(w http.ResponseWriter, r *http.Request) {
	project, target, err := s.loadPrepareTarget(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "targetID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildProjectTargetPreparePlan(project, target))
}

func (s *Server) handleProjectTargetPrepare(w http.ResponseWriter, r *http.Request) {
	if s.executor == nil {
		writeError(w, http.StatusServiceUnavailable, "EXECUTOR_UNAVAILABLE", "executor is unavailable")
		return
	}
	project, target, err := s.loadPrepareTarget(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "targetID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	s.refreshTargetReadiness(r.Context(), target)
	if target.Readiness == store.TargetReadinessChecking && target.LastPrepareRunID != "" {
		writeError(w, http.StatusConflict, "PREPARE_ACTIVE", "a prepare run is already active for this target")
		return
	}
	if strings.TrimSpace(target.PrepareCommand) == "" {
		writeError(w, http.StatusBadRequest, "PREPARE_NOT_CONFIGURED", "target has no prepare command")
		return
	}
	envVars := map[string]string{}
	if strings.TrimSpace(target.EnvJSON) != "" {
		if err := json.Unmarshal([]byte(target.EnvJSON), &envVars); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TARGET_ENV", "target env_json must be an object of string values")
			return
		}
	}
	prepareStartedAt := time.Now()
	acquired, err := s.store.BeginProjectTargetPrepare(r.Context(), target.ID, prepareStartedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if !acquired {
		writeError(w, http.StatusConflict, "PREPARE_ACTIVE", "a prepare run is already active for this target")
		return
	}
	target.Readiness = store.TargetReadinessChecking
	target.ReadinessObservedAt = &prepareStartedAt
	run, err := s.executor.SubmitAsync(r.Context(), executor.SubmitRequest{
		ResourceID: target.ResourceID, ProjectID: project.ID, TargetID: target.ID, RecipeName: "prepare",
		Name: "prepare " + project.Name + " / " + target.Name, Kind: store.RunKindSetup, GPUIndex: store.GPUIndexNone,
		Command: target.PrepareCommand, Cwd: target.Cwd, CondaEnv: target.CondaEnv, ProjectEnv: target.EnvStrategy,
		TargetEnv: target.DesiredEnv, UIEventsPath: target.UIEventsPath, EnvVars: envVars, CreatedBy: "ui-v2-launchpad",
		RefreshProjectEnv: true,
	}, executor.SubmitOptions{})
	if err != nil {
		now := time.Now()
		target.Readiness = store.TargetReadinessFailed
		target.ReadinessObservedAt = &now
		target.ReadinessError = err.Error()
		_ = s.store.SaveProjectTarget(r.Context(), target)
		writeError(w, http.StatusBadRequest, "PREPARE_SUBMIT_FAILED", err.Error())
		return
	}
	now := time.Now()
	target.Readiness = store.TargetReadinessChecking
	target.ReadinessObservedAt = &now
	target.ReadinessError = ""
	target.LastPrepareRunID = run.ID
	if err := s.store.SaveProjectTarget(r.Context(), target); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"target": target, "run": run, "plan": buildProjectTargetPreparePlan(project, target)})
}

func validTargetReadiness(value string) bool {
	switch value {
	case store.TargetReadinessUnknown, store.TargetReadinessChecking, store.TargetReadinessReady, store.TargetReadinessDrifted, store.TargetReadinessFailed:
		return true
	default:
		return false
	}
}

func (s *Server) handleSaveProjectTarget(w http.ResponseWriter, r *http.Request) {
	var target store.ProjectTarget
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	pathTargetID := ""
	if strings.HasPrefix(r.URL.Path, "/api/v1/project-targets/") {
		pathTargetID = chi.URLParam(r, "id")
	} else {
		target.ProjectID = chi.URLParam(r, "id")
	}
	if pathTargetID != "" {
		if target.ID != "" && target.ID != pathTargetID {
			writeError(w, http.StatusBadRequest, "ID_MISMATCH", "target id does not match URL")
			return
		}
		target.ID = pathTargetID
	}
	target.ID = strings.TrimSpace(target.ID)
	target.ProjectID = strings.TrimSpace(target.ProjectID)
	target.Name = strings.TrimSpace(target.Name)
	target.ResourceID = strings.TrimSpace(target.ResourceID)
	target.Cwd = strings.TrimSpace(target.Cwd)
	if target.ID == "" {
		target.ID = genID("target_")
	}
	if target.ProjectID == "" || target.Name == "" || target.ResourceID == "" || target.Cwd == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "project_id, name, resource_id, and cwd are required")
		return
	}
	if target.Readiness == "" {
		target.Readiness = store.TargetReadinessUnknown
	}
	if !validTargetReadiness(target.Readiness) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid target readiness")
		return
	}
	if project, err := s.store.GetProjectDefinition(r.Context(), target.ProjectID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if project == nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "project definition does not exist")
		return
	}
	if resource, err := s.store.GetResource(r.Context(), target.ResourceID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if resource == nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "resource does not exist")
		return
	}
	if err := s.store.SaveProjectTarget(r.Context(), &target); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, target)
}

func (s *Server) handleDeleteProjectTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteProjectTarget(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Legacy Project Evidence Aggregation ---

type projectRunCardView struct {
	store.ProjectRunCard
	Run   *store.Run      `json:"run,omitempty"`
	Marks []store.RunMark `json:"marks"`
}

type projectView struct {
	ProjectID             string               `json:"project_id"`
	ProjectName           string               `json:"project_name"`
	UpdatedAt             time.Time            `json:"updated_at"`
	TotalCards            int                  `json:"total_cards"`
	ImportantRuns         int                  `json:"important_runs"`
	PromotedRuns          int                  `json:"promoted_runs"`
	FormalRuns            int                  `json:"formal_runs"`
	RunningRuns           int                  `json:"running_runs"`
	PendingGraphProposals int                  `json:"pending_graph_proposals"`
	Cards                 []projectRunCardView `json:"cards"`
}

type projectCanonical struct {
	ID   string
	Name string
}

const unassignedProjectID = "__unassigned__"

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	views, err := s.projectViews(r.Context(), "", projectLimitFromQuery(r, 200))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	views, err := s.projectViews(r.Context(), projectID, projectLimitFromQuery(r, 500))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if len(views) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}
	writeJSON(w, http.StatusOK, views[0])
}

func projectLimitFromQuery(r *http.Request, def int) int {
	limit := def
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	return limit
}

func (s *Server) projectViews(ctx context.Context, projectID string, limit int) ([]projectView, error) {
	if limit <= 0 {
		limit = 200
	}
	cards, err := s.store.ListProjectRunCards(ctx, store.ProjectRunCardFilter{
		ProjectID: projectID,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	byProject := map[string]*projectView{}
	order := make([]string, 0)
	for _, card := range cards {
		id := strings.TrimSpace(card.ProjectID)
		if id == "" {
			id = unassignedProjectID
		}
		view := byProject[id]
		if view == nil {
			name := strings.TrimSpace(card.ProjectName)
			if name == "" {
				name = id
			}
			view = &projectView{ProjectID: id, ProjectName: name}
			byProject[id] = view
			order = append(order, id)
		}
		run, _ := s.store.GetRun(ctx, card.RunID)
		marks, _ := s.store.ListRunMarks(ctx, store.RunMarkFilter{RunID: card.RunID, Limit: 20})
		appendProjectRunCard(view, projectRunCardView{
			ProjectRunCard: card,
			Run:            run,
			Marks:          marks,
		})
	}
	if projectID == "" || projectID == unassignedProjectID {
		if err := s.appendUnassignedProjectRuns(ctx, byProject, &order, limit, nil); err != nil {
			return nil, err
		}
	}
	views := make([]projectView, 0, len(order))
	for _, id := range order {
		views = append(views, *byProject[id])
	}
	return views, nil
}

func projectAliasKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func manualProjectTarget(assignment store.RunProjectAssignment, aliases map[string]projectCanonical) projectCanonical {
	id := strings.TrimSpace(assignment.CategoryID)
	name := strings.TrimSpace(assignment.CategoryName)
	if name == "" {
		name = id
	}
	for _, key := range []string{id, name} {
		if canonical, ok := aliases[projectAliasKey(key)]; ok {
			return canonical
		}
	}
	return projectCanonical{ID: id, Name: name}
}

func (s *Server) appendManualProjectRuns(ctx context.Context, byProject map[string]*projectView, order *[]string, assignments []store.RunProjectAssignment, cardsByRun map[string]store.ProjectRunCard, projectAliases map[string]projectCanonical, projectID string) error {
	for _, assignment := range assignments {
		target := manualProjectTarget(assignment, projectAliases)
		if target.ID == "" {
			continue
		}
		if projectID != "" && projectID != assignment.CategoryID && projectID != target.ID {
			continue
		}
		view := byProject[target.ID]
		if view == nil {
			view = &projectView{ProjectID: target.ID, ProjectName: target.Name}
			byProject[target.ID] = view
			*order = append(*order, target.ID)
		}
		card := cardsByRun[assignment.RunID]
		if card.RunID == "" {
			card = store.ProjectRunCard{
				ID:        "manual_" + strings.TrimPrefix(assignment.RunID, "run_"),
				RunID:     assignment.RunID,
				UpdatedAt: assignment.UpdatedAt,
			}
		}
		card.ProjectID = target.ID
		card.ProjectName = target.Name
		if assignment.UpdatedAt.After(card.UpdatedAt) {
			card.UpdatedAt = assignment.UpdatedAt
		}
		run, _ := s.store.GetRun(ctx, assignment.RunID)
		marks, _ := s.store.ListRunMarks(ctx, store.RunMarkFilter{RunID: assignment.RunID, Limit: 20})
		appendProjectRunCard(view, projectRunCardView{
			ProjectRunCard: card,
			Run:            run,
			Marks:          marks,
		})
	}
	return nil
}

func (s *Server) appendUnassignedProjectRuns(ctx context.Context, byProject map[string]*projectView, order *[]string, limit int, manualByRun map[string]store.RunProjectAssignment) error {
	allCards, err := s.store.ListProjectRunCards(ctx, store.ProjectRunCardFilter{})
	if err != nil {
		return err
	}
	assigned := make(map[string]bool, len(allCards))
	for _, card := range allCards {
		if card.RunID != "" {
			assigned[card.RunID] = true
		}
	}
	for runID := range manualByRun {
		if runID != "" {
			assigned[runID] = true
		}
	}
	runs, err := s.store.ListRuns(ctx, store.RunFilter{Limit: limit})
	if err != nil {
		return err
	}
	view := byProject[unassignedProjectID]
	for _, run := range runs {
		if assigned[run.ID] {
			continue
		}
		if view == nil {
			view = &projectView{ProjectID: unassignedProjectID, ProjectName: "Unassigned runs"}
			byProject[unassignedProjectID] = view
			*order = append(*order, unassignedProjectID)
		}
		marks, _ := s.store.ListRunMarks(ctx, store.RunMarkFilter{RunID: run.ID, Limit: 20})
		runCopy := run
		appendProjectRunCard(view, projectRunCardView{
			ProjectRunCard: store.ProjectRunCard{
				ProjectID:     unassignedProjectID,
				ProjectName:   "Unassigned runs",
				RunID:         run.ID,
				EvidenceLevel: "",
				UpdatedAt:     run.CreatedAt,
			},
			Run:   &runCopy,
			Marks: marks,
		})
	}
	return nil
}

func appendProjectRunCard(view *projectView, card projectRunCardView) {
	view.Cards = append(view.Cards, card)
	view.TotalCards++
	if card.Important {
		view.ImportantRuns++
	}
	if card.ShouldPromote {
		view.PromotedRuns++
	}
	if card.GraphStatus == store.GraphProposalPending {
		view.PendingGraphProposals++
	}
	if card.UpdatedAt.After(view.UpdatedAt) {
		view.UpdatedAt = card.UpdatedAt
	}
	if card.Run != nil {
		kind := strings.ToLower(strings.TrimSpace(card.Run.Kind))
		if kind == "" || kind == store.RunKindFormal || kind == store.RunKindAblation {
			view.FormalRuns++
		}
		if store.IsRunRefreshableStatus(card.Run.Status) {
			view.RunningRuns++
		}
	}
}

type manualProjectCategoryRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type runManualProjectAssignmentRequest struct {
	CategoryID string `json:"category_id"`
}

func (s *Server) handleListManualProjectCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := s.store.ListManualProjectCategories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, categories)
}

func (s *Server) handleCreateManualProjectCategory(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "MANUAL_PROJECT_WRITE_DEPRECATED", "manual groups are read-only compatibility data; create or use a registered Project")
}

func (s *Server) handleListManualRunProjectAssignments(w http.ResponseWriter, r *http.Request) {
	assignments, err := s.store.ListRunProjectAssignments(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, assignments)
}

func (s *Server) handleAssignRunManualProjectCategory(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "MANUAL_PROJECT_WRITE_DEPRECATED", "manual groups are read-only compatibility data; assign the Run to a registered Project explicitly")
}

func (s *Server) handleUnassignRunManualProjectCategory(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.UnassignRunFromManualProjectCategory(r.Context(), runID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Automatic Run Comparability, Seed Aggregation, and Report ---

type runComparisonRequest struct {
	RunIDs    []string `json:"run_ids"`
	MetricKey string   `json:"metric_key,omitempty"`
}

type runComparisonIssue struct {
	Field    string            `json:"field"`
	Severity string            `json:"severity"`
	Values   map[string]string `json:"values"`
	Message  string            `json:"message"`
}

type runSeedAggregate struct {
	RunID     string             `json:"run_id"`
	MetricKey string             `json:"metric_key"`
	Seeds     map[string]float64 `json:"seeds"`
	Count     int                `json:"count"`
	Mean      float64            `json:"mean"`
	StdDev    float64            `json:"stddev"`
	Min       float64            `json:"min"`
	Max       float64            `json:"max"`
}

type runComparisonAnalysis struct {
	RunIDs                 []string             `json:"run_ids"`
	StructurallyComparable bool                 `json:"structurally_comparable"`
	ClaimReady             bool                 `json:"claim_ready"`
	Issues                 []runComparisonIssue `json:"issues"`
	Aggregates             []runSeedAggregate   `json:"aggregates"`
	ReportMarkdown         string               `json:"report_markdown"`
}

func (s *Server) handleAnalyzeRunComparison(w http.ResponseWriter, r *http.Request) {
	var req runComparisonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if len(req.RunIDs) < 2 || len(req.RunIDs) > 50 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "run_ids must contain between 2 and 50 runs")
		return
	}
	analysis, err := s.analyzeRunComparison(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "COMPARISON_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, analysis)
}

func (s *Server) analyzeRunComparison(ctx context.Context, req runComparisonRequest) (runComparisonAnalysis, error) {
	runs := make([]store.Run, 0, len(req.RunIDs))
	seen := map[string]bool{}
	for _, id := range req.RunIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		run, err := s.store.GetRun(ctx, id)
		if err != nil {
			return runComparisonAnalysis{}, err
		}
		if run == nil {
			return runComparisonAnalysis{}, fmt.Errorf("run %s not found", id)
		}
		runs = append(runs, *run)
	}
	if len(runs) < 2 {
		return runComparisonAnalysis{}, fmt.Errorf("at least two distinct runs are required")
	}
	analysis := runComparisonAnalysis{StructurallyComparable: true, ClaimReady: true}
	for _, run := range runs {
		analysis.RunIDs = append(analysis.RunIDs, run.ID)
	}
	compareRunField := func(field string, critical bool, value func(store.Run) string) {
		values := map[string]string{}
		unique := map[string]bool{}
		for _, run := range runs {
			values[run.ID] = value(run)
			unique[value(run)] = true
		}
		if len(unique) > 1 {
			severity := "warning"
			if critical {
				severity = "error"
				analysis.StructurallyComparable = false
			}
			analysis.ClaimReady = false
			analysis.Issues = append(analysis.Issues, runComparisonIssue{Field: field, Severity: severity, Values: values, Message: field + " differs across runs"})
		}
	}
	compareRunField("project_id", true, func(run store.Run) string { return run.ProjectID })
	compareRunField("recipe_name", true, func(run store.Run) string { return run.RecipeName })
	compareRunField("task_role", true, func(run store.Run) string { return run.TaskRole })
	compareRunField("git_commit", false, func(run store.Run) string { return run.GitCommit })
	compareRunField("resolved_env", false, func(run store.Run) string { return run.ResolvedEnv + "|" + run.ResolvedPython })
	for _, run := range runs {
		if run.EvidenceGrade != store.RunEvidenceGradeFormal {
			analysis.ClaimReady = false
			analysis.Issues = append(analysis.Issues, runComparisonIssue{Field: "evidence_grade", Severity: "error", Values: map[string]string{run.ID: run.EvidenceGrade}, Message: "non-formal runs cannot support a formal comparison claim"})
		}
		manifest, _ := s.store.GetRunManifest(ctx, run.ID)
		if manifest == nil || manifest.State != store.RunManifestFinal {
			analysis.ClaimReady = false
			analysis.Issues = append(analysis.Issues, runComparisonIssue{Field: "manifest", Severity: "warning", Values: map[string]string{run.ID: "missing_or_draft"}, Message: "run has no finalized reproducibility manifest"})
		}
	}
	analysis.Aggregates = s.aggregateRunSeeds(ctx, runs, strings.TrimSpace(req.MetricKey))
	if len(analysis.Aggregates) == 0 {
		analysis.ClaimReady = false
		analysis.Issues = append(analysis.Issues, runComparisonIssue{Field: "metrics", Severity: "warning", Values: map[string]string{}, Message: "no structured seed metrics were found"})
	}
	for _, aggregate := range analysis.Aggregates {
		if aggregate.Count < 2 {
			analysis.ClaimReady = false
			analysis.Issues = append(analysis.Issues, runComparisonIssue{Field: "seed_count", Severity: "warning", Values: map[string]string{aggregate.RunID: strconv.Itoa(aggregate.Count)}, Message: "seed aggregation has fewer than two seeds"})
		}
	}
	analysis.ReportMarkdown = renderRunComparisonReport(analysis, runs)
	return analysis, nil
}

func (s *Server) aggregateRunSeeds(ctx context.Context, runs []store.Run, metricFilter string) []runSeedAggregate {
	type key struct{ runID, metric string }
	values := map[key]map[string]float64{}
	for _, run := range runs {
		path := strings.TrimSpace(run.UIEventsPath)
		if path == "" {
			continue
		}
		lines, _, _, _ := s.remoteLogFileLines(ctx, run.ID, path, 10000)
		for _, line := range lines {
			var event map[string]interface{}
			if json.Unmarshal([]byte(line.Content), &event) != nil {
				continue
			}
			metric := firstNonEmptyString(eventText(event, "name"), eventText(event, "metric"), eventText(event, "key"), eventText(event, "label"))
			if metric == "" || metricFilter != "" && metric != metricFilter {
				continue
			}
			value, ok := eventNumber(event["value"])
			if !ok {
				continue
			}
			seed := eventText(event, "seed")
			if seed == "" {
				seed = "unspecified"
			}
			k := key{runID: run.ID, metric: metric}
			if values[k] == nil {
				values[k] = map[string]float64{}
			}
			values[k][seed] = value
		}
	}
	keys := make([]key, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].metric == keys[j].metric {
			return keys[i].runID < keys[j].runID
		}
		return keys[i].metric < keys[j].metric
	})
	aggregates := make([]runSeedAggregate, 0, len(keys))
	for _, k := range keys {
		seeds := values[k]
		aggregate := runSeedAggregate{RunID: k.runID, MetricKey: k.metric, Seeds: seeds, Count: len(seeds), Min: math.Inf(1), Max: math.Inf(-1)}
		for _, value := range seeds {
			aggregate.Mean += value
			aggregate.Min = math.Min(aggregate.Min, value)
			aggregate.Max = math.Max(aggregate.Max, value)
		}
		aggregate.Mean /= float64(aggregate.Count)
		for _, value := range seeds {
			delta := value - aggregate.Mean
			aggregate.StdDev += delta * delta
		}
		if aggregate.Count > 1 {
			aggregate.StdDev = math.Sqrt(aggregate.StdDev / float64(aggregate.Count-1))
		}
		aggregates = append(aggregates, aggregate)
	}
	return aggregates
}

func eventText(event map[string]interface{}, key string) string {
	value, ok := event[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func eventNumber(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func renderRunComparisonReport(analysis runComparisonAnalysis, runs []store.Run) string {
	var report strings.Builder
	report.WriteString("# aexp comparison report\n\n")
	fmt.Fprintf(&report, "- Structurally comparable: **%t**\n- Claim ready: **%t**\n- Runs: %s\n\n", analysis.StructurallyComparable, analysis.ClaimReady, strings.Join(analysis.RunIDs, ", "))
	if len(analysis.Issues) > 0 {
		report.WriteString("## Comparability checks\n\n")
		for _, issue := range analysis.Issues {
			fmt.Fprintf(&report, "- **%s** `%s`: %s\n", issue.Severity, issue.Field, issue.Message)
		}
	}
	if len(analysis.Aggregates) > 0 {
		report.WriteString("\n## Seed aggregates\n\n| Run | Metric | n | Mean | StdDev | Min | Max |\n|---|---|---:|---:|---:|---:|---:|\n")
		for _, aggregate := range analysis.Aggregates {
			fmt.Fprintf(&report, "| %s | %s | %d | %.6g | %.6g | %.6g | %.6g |\n", aggregate.RunID, aggregate.MetricKey, aggregate.Count, aggregate.Mean, aggregate.StdDev, aggregate.Min, aggregate.Max)
		}
	}
	report.WriteString("\n## Provenance boundary\n\nThis report summarizes recorded manifests and structured events. It does not turn smoke or pilot runs into formal evidence.\n")
	return report.String()
}

// --- Experiment Matrices ---

type experimentMatrixRequest struct {
	Title             string `json:"title"`
	Description       string `json:"description"`
	SourceKind        string `json:"source_kind"`
	SourceID          string `json:"source_id"`
	SourceName        string `json:"source_name"`
	DefaultMetricKey  string `json:"default_metric_key"`
	DefaultMetricGoal string `json:"default_metric_goal"`
	DataJSON          string `json:"data_json"`
	SeedFromSource    bool   `json:"seed_from_source"`
}

func (s *Server) handleListExperimentMatrices(w http.ResponseWriter, r *http.Request) {
	matrices, err := s.store.ListExperimentMatrices(r.Context(), store.ExperimentMatrixFilter{
		Query:      r.URL.Query().Get("query"),
		SourceKind: r.URL.Query().Get("source_kind"),
		SourceID:   r.URL.Query().Get("source_id"),
		Limit:      projectLimitFromQuery(r, 200),
		Offset:     parseIntQuery(r.URL.Query().Get("offset"), 0),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, matrices)
}

func (s *Server) handleCreateExperimentMatrix(w http.ResponseWriter, r *http.Request) {
	var req experimentMatrixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	matrix := store.ExperimentMatrix{
		ID:                genID("matrix_"),
		Title:             strings.TrimSpace(req.Title),
		Description:       strings.TrimSpace(req.Description),
		SourceKind:        strings.TrimSpace(req.SourceKind),
		SourceID:          strings.TrimSpace(req.SourceID),
		SourceName:        strings.TrimSpace(req.SourceName),
		DefaultMetricKey:  strings.TrimSpace(req.DefaultMetricKey),
		DefaultMetricGoal: strings.TrimSpace(req.DefaultMetricGoal),
		DataJSON:          normalizeJSONText(req.DataJSON),
	}
	if matrix.Title == "" {
		matrix.Title = matrix.SourceName
	}
	if matrix.Title == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "title is required")
		return
	}
	if err := s.store.CreateExperimentMatrix(r.Context(), &matrix); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if req.SeedFromSource {
		grid, err := s.seedExperimentMatrixGrid(r.Context(), matrix)
		if err != nil {
			writeError(w, http.StatusBadRequest, "SEED_FAILED", err.Error())
			return
		}
		if err := s.store.SaveExperimentMatrixGrid(r.Context(), matrix.ID, grid); err != nil {
			writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
	}
	detail, ok := s.experimentMatrixDetail(r.Context(), matrix.ID, w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) handleGetExperimentMatrix(w http.ResponseWriter, r *http.Request) {
	matrixID := chi.URLParam(r, "id")
	detail, ok := s.experimentMatrixDetail(r.Context(), matrixID, w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) experimentMatrixDetail(ctx context.Context, matrixID string, w http.ResponseWriter) (store.ExperimentMatrixDetail, bool) {
	matrix, err := s.store.GetExperimentMatrix(ctx, matrixID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return store.ExperimentMatrixDetail{}, false
	}
	if matrix == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "experiment matrix not found")
		return store.ExperimentMatrixDetail{}, false
	}
	grid, err := s.store.GetExperimentMatrixGrid(ctx, matrixID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return store.ExperimentMatrixDetail{}, false
	}
	return store.ExperimentMatrixDetail{ExperimentMatrix: *matrix, Rows: grid.Rows, Columns: grid.Columns, Cells: grid.Cells}, true
}

func (s *Server) handleUpdateExperimentMatrix(w http.ResponseWriter, r *http.Request) {
	matrixID := chi.URLParam(r, "id")
	existing, err := s.store.GetExperimentMatrix(r.Context(), matrixID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "experiment matrix not found")
		return
	}
	var req experimentMatrixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	existing.Title = strings.TrimSpace(req.Title)
	existing.Description = strings.TrimSpace(req.Description)
	existing.SourceKind = strings.TrimSpace(req.SourceKind)
	existing.SourceID = strings.TrimSpace(req.SourceID)
	existing.SourceName = strings.TrimSpace(req.SourceName)
	existing.DefaultMetricKey = strings.TrimSpace(req.DefaultMetricKey)
	existing.DefaultMetricGoal = strings.TrimSpace(req.DefaultMetricGoal)
	existing.DataJSON = normalizeJSONText(req.DataJSON)
	if existing.Title == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "title is required")
		return
	}
	if err := s.store.UpdateExperimentMatrix(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteExperimentMatrix(w http.ResponseWriter, r *http.Request) {
	matrixID := chi.URLParam(r, "id")
	if err := s.store.DeleteExperimentMatrix(r.Context(), matrixID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSaveExperimentMatrixGrid(w http.ResponseWriter, r *http.Request) {
	matrixID := chi.URLParam(r, "id")
	if matrix, err := s.store.GetExperimentMatrix(r.Context(), matrixID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if matrix == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "experiment matrix not found")
		return
	}
	var grid store.ExperimentMatrixGrid
	if err := json.NewDecoder(r.Body).Decode(&grid); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if err := s.validateExperimentMatrixGrid(r.Context(), matrixID, &grid); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_GRID", err.Error())
		return
	}
	if err := s.store.SaveExperimentMatrixGrid(r.Context(), matrixID, grid); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	detail, ok := s.experimentMatrixDetail(r.Context(), matrixID, w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) validateExperimentMatrixGrid(ctx context.Context, matrixID string, grid *store.ExperimentMatrixGrid) error {
	rowIDs := make(map[string]bool, len(grid.Rows))
	for i := range grid.Rows {
		row := &grid.Rows[i]
		row.ID = strings.TrimSpace(row.ID)
		row.MatrixID = matrixID
		row.Label = strings.TrimSpace(row.Label)
		row.DataJSON = normalizeJSONText(row.DataJSON)
		if row.ID == "" {
			return fmt.Errorf("row id is required")
		}
		if row.Label == "" {
			return fmt.Errorf("row %q label is required", row.ID)
		}
		if rowIDs[row.ID] {
			return fmt.Errorf("duplicate row id %q", row.ID)
		}
		rowIDs[row.ID] = true
	}
	columnIDs := make(map[string]bool, len(grid.Columns))
	for i := range grid.Columns {
		column := &grid.Columns[i]
		column.ID = strings.TrimSpace(column.ID)
		column.MatrixID = matrixID
		column.Label = strings.TrimSpace(column.Label)
		column.DataJSON = normalizeJSONText(column.DataJSON)
		if column.ID == "" {
			return fmt.Errorf("column id is required")
		}
		if column.Label == "" {
			return fmt.Errorf("column %q label is required", column.ID)
		}
		if columnIDs[column.ID] {
			return fmt.Errorf("duplicate column id %q", column.ID)
		}
		columnIDs[column.ID] = true
	}
	cellIDs := make(map[string]bool, len(grid.Cells))
	for i := range grid.Cells {
		cell := &grid.Cells[i]
		cell.ID = strings.TrimSpace(cell.ID)
		cell.MatrixID = matrixID
		cell.RowID = strings.TrimSpace(cell.RowID)
		cell.ColumnID = strings.TrimSpace(cell.ColumnID)
		cell.RunID = strings.TrimSpace(cell.RunID)
		cell.ProjectCardID = strings.TrimSpace(cell.ProjectCardID)
		cell.DataJSON = normalizeJSONText(cell.DataJSON)
		if cell.ID == "" {
			return fmt.Errorf("cell id is required")
		}
		if cellIDs[cell.ID] {
			return fmt.Errorf("duplicate cell id %q", cell.ID)
		}
		if !rowIDs[cell.RowID] {
			return fmt.Errorf("cell %q row %q does not exist", cell.ID, cell.RowID)
		}
		if !columnIDs[cell.ColumnID] {
			return fmt.Errorf("cell %q column %q does not exist", cell.ID, cell.ColumnID)
		}
		if cell.RunID != "" {
			run, err := s.store.GetRun(ctx, cell.RunID)
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("run %q does not exist", cell.RunID)
			}
		}
		cellIDs[cell.ID] = true
	}
	return nil
}

func (s *Server) seedExperimentMatrixGrid(ctx context.Context, matrix store.ExperimentMatrix) (store.ExperimentMatrixGrid, error) {
	column := store.ExperimentMatrixColumn{ID: genID("mcol_"), Label: "结果", Position: 0}
	grid := store.ExperimentMatrixGrid{Columns: []store.ExperimentMatrixColumn{column}}
	switch matrix.SourceKind {
	case "project":
		cards, err := s.store.ListProjectRunCards(ctx, store.ProjectRunCardFilter{ProjectID: matrix.SourceID, Limit: 500})
		if err != nil {
			return grid, err
		}
		for i, card := range cards {
			row := store.ExperimentMatrixRow{ID: genID("mrow_"), Label: firstNonEmpty(card.Question, card.Verdict, card.RunID, card.ID), Position: i}
			cell := store.ExperimentMatrixCell{
				ID:            genID("mcell_"),
				RowID:         row.ID,
				ColumnID:      column.ID,
				RunID:         card.RunID,
				ProjectCardID: card.ID,
				Title:         firstNonEmpty(card.Verdict, card.Question, card.RunID),
				Statement:     firstNonEmpty(card.KeyMetrics, card.SupportsClaim, card.WeakensClaim, card.NextAction),
				MetricKey:     matrix.DefaultMetricKey,
			}
			grid.Rows = append(grid.Rows, row)
			grid.Cells = append(grid.Cells, cell)
		}
	case "manual_project":
		assignments, err := s.store.ListRunProjectAssignments(ctx)
		if err != nil {
			return grid, err
		}
		pos := 0
		for _, assignment := range assignments {
			if assignment.CategoryID != matrix.SourceID {
				continue
			}
			run, _ := s.store.GetRun(ctx, assignment.RunID)
			label := assignment.RunID
			if run != nil {
				label = firstNonEmpty(run.Name, run.ID)
			}
			row := store.ExperimentMatrixRow{ID: genID("mrow_"), Label: label, Position: pos}
			cell := store.ExperimentMatrixCell{ID: genID("mcell_"), RowID: row.ID, ColumnID: column.ID, RunID: assignment.RunID, Title: label, MetricKey: matrix.DefaultMetricKey}
			grid.Rows = append(grid.Rows, row)
			grid.Cells = append(grid.Cells, cell)
			pos++
		}
	case "":
		grid.Rows = []store.ExperimentMatrixRow{{ID: genID("mrow_"), Label: "实验", Position: 0}}
	default:
		return grid, fmt.Errorf("unsupported source_kind %q", matrix.SourceKind)
	}
	if len(grid.Rows) == 0 {
		grid.Rows = []store.ExperimentMatrixRow{{ID: genID("mrow_"), Label: "待归类", Position: 0}}
	}
	return grid, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// --- Evidence Chains ---

type evidenceChainDetail struct {
	store.EvidenceChain
	Nodes []store.EvidenceChainNode `json:"nodes"`
	Edges []store.EvidenceChainEdge `json:"edges"`
}

type evidenceChainUpdateRequest struct {
	Title        string                           `json:"title"`
	Description  string                           `json:"description"`
	RoutingHints *store.EvidenceGraphRoutingHints `json:"routing_hints"`
	ProjectID    string                           `json:"project_id"`
	Role         string                           `json:"role"`
	Status       string                           `json:"status"`
}

func (s *Server) handleListEvidenceChains(w http.ResponseWriter, r *http.Request) {
	chains, err := s.store.ListEvidenceChains(r.Context(), store.EvidenceChainFilter{
		Query:     r.URL.Query().Get("query"),
		ProjectID: r.URL.Query().Get("project_id"),
		Role:      r.URL.Query().Get("role"),
		Status:    r.URL.Query().Get("status"),
		Limit:     projectLimitFromQuery(r, 200),
		Offset:    parseIntQuery(r.URL.Query().Get("offset"), 0),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chains)
}

func (s *Server) handleCreateEvidenceChain(w http.ResponseWriter, r *http.Request) {
	var req store.EvidenceChain
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "title is required")
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "active evidence maps must belong to a registered project")
		return
	}
	project, err := s.store.GetProjectDefinition(r.Context(), req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusBadRequest, "PROJECT_NOT_REGISTERED", "project_id must reference a registered project")
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = genID("chain_")
	}
	if err := s.store.CreateEvidenceChain(r.Context(), &req); err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleCreateProjectEvidenceMap(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	project, err := s.store.GetProjectDefinition(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "PROJECT_NOT_REGISTERED", "project not found")
		return
	}
	var req store.EvidenceChain
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "title is required")
		return
	}
	req.ProjectID = projectID
	req.Role = "secondary"
	req.Status = "active"
	if strings.TrimSpace(req.ID) == "" {
		req.ID = genID("chain_")
	}
	if err := s.store.CreateEvidenceChain(r.Context(), &req); err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleGetEvidenceChain(w http.ResponseWriter, r *http.Request) {
	chainID := chi.URLParam(r, "id")
	detail, ok := s.evidenceChainDetail(r.Context(), chainID, w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) evidenceChainDetail(ctx context.Context, chainID string, w http.ResponseWriter) (evidenceChainDetail, bool) {
	chain, err := s.store.GetEvidenceChain(ctx, chainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return evidenceChainDetail{}, false
	}
	if chain == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence chain not found")
		return evidenceChainDetail{}, false
	}
	graph, err := s.store.GetEvidenceChainGraph(ctx, chainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return evidenceChainDetail{}, false
	}
	return evidenceChainDetail{EvidenceChain: *chain, Nodes: graph.Nodes, Edges: graph.Edges}, true
}

func (s *Server) handleUpdateEvidenceChain(w http.ResponseWriter, r *http.Request) {
	chainID := chi.URLParam(r, "id")
	existing, err := s.store.GetEvidenceChain(r.Context(), chainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence chain not found")
		return
	}
	wasPrimary := existing.Role == "primary"
	var req evidenceChainUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	existing.Title = strings.TrimSpace(req.Title)
	existing.Description = strings.TrimSpace(req.Description)
	if req.RoutingHints != nil {
		existing.RoutingHints = *req.RoutingHints
	}
	if projectID := strings.TrimSpace(req.ProjectID); projectID != "" {
		project, projectErr := s.store.GetProjectDefinition(r.Context(), projectID)
		if projectErr != nil {
			writeError(w, http.StatusInternalServerError, "DB_ERROR", projectErr.Error())
			return
		}
		if project == nil {
			writeError(w, http.StatusNotFound, "PROJECT_NOT_REGISTERED", "project_id must reference a registered project")
			return
		}
		existing.ProjectID = projectID
	}
	if role := strings.TrimSpace(req.Role); role != "" {
		if role != "primary" && role != "secondary" && role != "archive" {
			writeError(w, http.StatusBadRequest, "INVALID_ROLE", "role must be primary, secondary, or archive")
			return
		}
		existing.Role = role
	}
	if status := strings.TrimSpace(req.Status); status != "" {
		if status != "active" && status != "archived" {
			writeError(w, http.StatusBadRequest, "INVALID_STATUS", "status must be active or archived")
			return
		}
		existing.Status = status
	}
	if wasPrimary && (existing.Role != "primary" || existing.Status != "active") {
		writeError(w, http.StatusConflict, "PRIMARY_MAP_REQUIRED", "the Project Primary Map must remain active")
		return
	}
	if existing.Title == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "title is required")
		return
	}
	if existing.Status == "active" && strings.TrimSpace(existing.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "active evidence maps must belong to a registered project")
		return
	}
	if err := s.store.UpdateEvidenceChain(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteEvidenceChain(w http.ResponseWriter, r *http.Request) {
	chainID := chi.URLParam(r, "id")
	chain, err := s.store.GetEvidenceChain(r.Context(), chainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if chain == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence Map not found")
		return
	}
	if chain.Role == "primary" {
		writeError(w, http.StatusConflict, "PRIMARY_MAP_REQUIRED", "the Project Primary Map cannot be archived or deleted")
		return
	}
	if r.URL.Query().Get("permanent") == "true" {
		purger, ok := s.store.(interface {
			PurgeEvidenceChain(context.Context, string) error
		})
		if !ok {
			writeError(w, http.StatusNotImplemented, "PURGE_UNAVAILABLE", "permanent Evidence Map deletion is unavailable")
			return
		}
		if err := purger.PurgeEvidenceChain(r.Context(), chainID); err != nil {
			writeEvidenceGraphError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.DeleteEvidenceChain(r.Context(), chainID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuditEvidenceChain(w http.ResponseWriter, r *http.Request) {
	report, err := s.store.AuditEvidenceChain(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	if report == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence chain not found")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleSaveEvidenceChainGraph(w http.ResponseWriter, r *http.Request) {
	chainID := chi.URLParam(r, "id")
	if chain, err := s.store.GetEvidenceChain(r.Context(), chainID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if chain == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence chain not found")
		return
	}
	var request struct {
		ExpectedRevision *int64                    `json:"expected_revision"`
		Nodes            []store.EvidenceChainNode `json:"nodes"`
		Edges            []store.EvidenceChainEdge `json:"edges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "EXPECTED_REVISION_REQUIRED", "layout save requires expected_revision")
		return
	}
	expectedRevision := *request.ExpectedRevision
	graph := store.EvidenceChainGraph{Nodes: request.Nodes, Edges: request.Edges}
	if _, err := s.store.SaveEvidenceChainGraphCAS(r.Context(), chainID, graph, store.EvidenceGraphSaveOptions{
		ExpectedRevision: expectedRevision,
		Actor:            "ui",
		SourceKind:       "replace_graph",
	}); err != nil {
		var conflict *store.EvidenceGraphRevisionConflict
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":              "REVISION_CONFLICT",
				"details":            conflict.Error(),
				"expected_revision":  conflict.Expected,
				"current_revision":   conflict.Current,
				"current_graph_hash": conflict.CurrentHash,
			})
			return
		}
		var validation *store.EvidenceGraphValidationError
		if errors.As(err, &validation) {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":   validation.Code,
				"details": validation.Message,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	detail, ok := s.evidenceChainDetail(r.Context(), chainID, w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleListEvidenceChainRevisions(w http.ResponseWriter, r *http.Request) {
	chainID := chi.URLParam(r, "id")
	if chain, err := s.store.GetEvidenceChain(r.Context(), chainID); err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	} else if chain == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence chain not found")
		return
	}
	revisions, err := s.store.ListEvidenceChainRevisions(r.Context(), chainID, projectLimitFromQuery(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, revisions)
}

func (s *Server) handleGetEvidenceChainRevision(w http.ResponseWriter, r *http.Request) {
	chainID := chi.URLParam(r, "id")
	revisionNumber, err := strconv.ParseInt(chi.URLParam(r, "revision"), 10, 64)
	if err != nil || revisionNumber < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REVISION", "revision must be a non-negative integer")
		return
	}
	revision, err := s.store.GetEvidenceChainRevision(r.Context(), chainID, revisionNumber)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if revision == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence chain revision not found")
		return
	}
	writeJSON(w, http.StatusOK, revision)
}

func (s *Server) validateEvidenceChainGraph(ctx context.Context, chainID string, graph *store.EvidenceChainGraph) error {
	nodeIDs := make(map[string]bool, len(graph.Nodes))
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		node.ID = strings.TrimSpace(node.ID)
		node.ChainID = chainID
		node.Type = strings.TrimSpace(node.Type)
		node.RunID = strings.TrimSpace(node.RunID)
		node.ProjectCardID = strings.TrimSpace(node.ProjectCardID)
		node.DataJSON = normalizeJSONText(node.DataJSON)
		if node.ID == "" {
			return fmt.Errorf("node id is required")
		}
		if nodeIDs[node.ID] {
			return fmt.Errorf("duplicate node id %q", node.ID)
		}
		if !validEvidenceNodeType(node.Type) {
			return fmt.Errorf("invalid node type %q", node.Type)
		}
		if node.Type == store.EvidenceNodeRun {
			if node.RunID == "" {
				return fmt.Errorf("run node %q requires run_id", node.ID)
			}
			run, err := s.store.GetRun(ctx, node.RunID)
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("run %q does not exist", node.RunID)
			}
		}
		if node.Width <= 0 {
			node.Width = 260
		}
		if node.Height <= 0 {
			node.Height = 140
		}
		nodeIDs[node.ID] = true
	}
	edgeIDs := make(map[string]bool, len(graph.Edges))
	for i := range graph.Edges {
		edge := &graph.Edges[i]
		edge.ID = strings.TrimSpace(edge.ID)
		edge.ChainID = chainID
		edge.SourceNodeID = strings.TrimSpace(edge.SourceNodeID)
		edge.TargetNodeID = strings.TrimSpace(edge.TargetNodeID)
		edge.Type = strings.TrimSpace(edge.Type)
		edge.DataJSON = normalizeJSONText(edge.DataJSON)
		if edge.ID == "" {
			return fmt.Errorf("edge id is required")
		}
		if edgeIDs[edge.ID] {
			return fmt.Errorf("duplicate edge id %q", edge.ID)
		}
		if !validEvidenceEdgeType(edge.Type) {
			return fmt.Errorf("invalid edge type %q", edge.Type)
		}
		if !nodeIDs[edge.SourceNodeID] {
			return fmt.Errorf("edge %q source node %q does not exist", edge.ID, edge.SourceNodeID)
		}
		if !nodeIDs[edge.TargetNodeID] {
			return fmt.Errorf("edge %q target node %q does not exist", edge.ID, edge.TargetNodeID)
		}
		edgeIDs[edge.ID] = true
	}
	return nil
}

func normalizeJSONText(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

func validEvidenceNodeType(t string) bool {
	return store.ValidEvidenceNodeType(t)
}

func validEvidenceEdgeType(t string) bool {
	return store.ValidEvidenceEdgeType(t)
}

func (s *Server) handleListEvidenceRunCandidates(w http.ResponseWriter, r *http.Request) {
	candidates, err := s.store.ListEvidenceRunCandidates(r.Context(), store.EvidenceRunCandidateFilter{
		Query: r.URL.Query().Get("query"),
		Limit: projectLimitFromQuery(r, 80),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

type createEvidenceProposalRequest struct {
	TargetMapID        string                    `json:"target_map_id"`
	Actor              string                    `json:"actor"`
	Summary            string                    `json:"summary"`
	RoutingReason      string                    `json:"routing_reason"`
	ProjectLevelImpact bool                      `json:"project_level_impact"`
	SourceRunIDs       []string                  `json:"source_run_ids"`
	SourceSnapshotIDs  []string                  `json:"source_snapshot_ids"`
	Patch              *store.EvidenceGraphPatch `json:"patch"`
}

type evidenceProposalView struct {
	store.EvidenceProposal
	TargetMap *store.EvidenceChain `json:"target_map,omitempty"`
}

func (s *Server) evidenceProposalView(ctx context.Context, proposal *store.EvidenceProposal) (evidenceProposalView, error) {
	view := evidenceProposalView{EvidenceProposal: *proposal}
	if proposal.TargetChainID == "" {
		return view, nil
	}
	target, err := s.store.GetEvidenceChain(ctx, proposal.TargetChainID)
	if err != nil {
		return evidenceProposalView{}, err
	}
	view.TargetMap = target
	return view, nil
}

func (s *Server) handleCreateProjectEvidenceProposal(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	var req createEvidenceProposalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	proposal, err := s.store.CreateEvidenceProposal(r.Context(), &store.EvidenceProposal{
		ProjectID:          projectID,
		TargetChainID:      strings.TrimSpace(req.TargetMapID),
		Actor:              strings.TrimSpace(req.Actor),
		Summary:            strings.TrimSpace(req.Summary),
		RoutingReason:      strings.TrimSpace(req.RoutingReason),
		ProjectLevelImpact: req.ProjectLevelImpact,
		SourceRunIDs:       req.SourceRunIDs,
		SourceSnapshotIDs:  req.SourceSnapshotIDs,
	}, req.Patch)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	view, err := s.evidenceProposalView(r.Context(), proposal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handleListProjectEvidenceProposals(w http.ResponseWriter, r *http.Request) {
	proposals, err := s.store.ListEvidenceProposals(r.Context(), store.EvidenceProposalFilter{
		ProjectID: strings.TrimSpace(chi.URLParam(r, "id")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:     projectLimitFromQuery(r, 80),
		Offset:    parseIntQuery(r.URL.Query().Get("offset"), 0),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	views := make([]evidenceProposalView, 0, len(proposals))
	for index := range proposals {
		view, viewErr := s.evidenceProposalView(r.Context(), &proposals[index])
		if viewErr != nil {
			writeError(w, http.StatusInternalServerError, "DB_ERROR", viewErr.Error())
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetEvidenceProposal(w http.ResponseWriter, r *http.Request) {
	proposal, err := s.store.GetEvidenceProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if proposal == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence proposal not found")
		return
	}
	view, err := s.evidenceProposalView(r.Context(), proposal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handlePlanEvidenceProposal(w http.ResponseWriter, r *http.Request) {
	plan, err := s.store.PlanEvidenceProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	if plan == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence proposal not found")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleReviewEvidenceProposal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action   string `json:"action"`
		Reviewer string `json:"reviewer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	proposal, err := s.store.ReviewEvidenceProposal(r.Context(), chi.URLParam(r, "id"), req.Action, req.Reviewer)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	view, err := s.evidenceProposalView(r.Context(), proposal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRerouteEvidenceProposal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetMapID        string `json:"target_map_id"`
		RoutingReason      string `json:"routing_reason"`
		ProjectLevelImpact bool   `json:"project_level_impact"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	proposal, err := s.store.RerouteEvidenceProposal(
		r.Context(), chi.URLParam(r, "id"), req.TargetMapID, req.RoutingReason, req.ProjectLevelImpact,
	)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	view, err := s.evidenceProposalView(r.Context(), proposal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handlePlanEvidencePromotion(w http.ResponseWriter, r *http.Request) {
	var req store.EvidencePromotionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	req.SourceMapID = strings.TrimSpace(chi.URLParam(r, "id"))
	plan, err := s.store.PlanEvidencePromotion(r.Context(), req)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleCreateEvidencePromotion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.EvidencePromotionRequest
		ExpectedPlanHash string `json:"expected_plan_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	req.SourceMapID = strings.TrimSpace(chi.URLParam(r, "id"))
	proposal, err := s.store.CreateEvidencePromotion(r.Context(), req.EvidencePromotionRequest, req.ExpectedPlanHash)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	view, err := s.evidenceProposalView(r.Context(), proposal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handleGetEvidenceGraphProposal(w http.ResponseWriter, r *http.Request) {
	card, err := s.store.GetProjectRunCard(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if card == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project run card not found")
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Server) handleSaveProjectRunCard(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	var req struct {
		Card              store.ProjectRunCard `json:"card"`
		ExpectedUpdatedAt *time.Time           `json:"expected_updated_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	existing, err := s.store.GetProjectRunCard(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if existing != nil && req.ExpectedUpdatedAt == nil {
		writeError(w, http.StatusBadRequest, "EXPECTED_REVISION_REQUIRED", "expected_updated_at is required when updating an existing project card")
		return
	}
	req.Card.RunID = runID
	if existing != nil {
		req.Card.ID = existing.ID
		req.Card.ProjectID = existing.ProjectID
		req.Card.ProjectName = existing.ProjectName
		req.Card.CreatedAt = existing.CreatedAt
		req.Card.UpdatedAt = *req.ExpectedUpdatedAt
	}
	if err := s.store.SaveProjectRunCard(r.Context(), &req.Card); err != nil {
		var conflict *store.ProjectRunCardRevisionConflict
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":               "REVISION_CONFLICT",
				"details":             conflict.Error(),
				"run_id":              conflict.RunID,
				"expected_updated_at": conflict.Expected,
				"current_updated_at":  conflict.Current,
			})
			return
		}
		writeEvidenceGraphError(w, err)
		return
	}
	saved, err := s.store.GetProjectRunCard(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	status := http.StatusOK
	if existing == nil {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

func (s *Server) handleSubmitEvidenceGraphProposal(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	var req struct {
		Card              store.ProjectRunCard      `json:"card"`
		Patch             *store.EvidenceGraphPatch `json:"patch,omitempty"`
		NoGraphImpact     bool                      `json:"no_graph_impact"`
		GraphImpactReason string                    `json:"graph_impact_reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	req.Card.RunID = runID
	if req.NoGraphImpact {
		req.Card.NoGraphImpact = true
		req.Card.GraphImpactReason = strings.TrimSpace(req.GraphImpactReason)
	}
	saved, err := s.store.SubmitEvidenceGraphProposal(r.Context(), &req.Card, req.Patch)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, saved)
}

func (s *Server) handlePlanEvidenceGraphProposal(w http.ResponseWriter, r *http.Request) {
	plan, err := s.store.PlanEvidenceGraphProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	if plan == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project run card not found")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleReviewEvidenceGraphProposal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action   string `json:"action"`
		Reviewer string `json:"reviewer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	card, err := s.store.ReviewEvidenceGraphProposal(r.Context(), chi.URLParam(r, "id"), req.Action, req.Reviewer)
	if err != nil {
		writeEvidenceGraphError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func writeEvidenceGraphError(w http.ResponseWriter, err error) {
	var conflict *store.EvidenceGraphRevisionConflict
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":              "REVISION_CONFLICT",
			"details":            conflict.Error(),
			"expected_revision":  conflict.Expected,
			"current_revision":   conflict.Current,
			"current_graph_hash": conflict.CurrentHash,
		})
		return
	}
	var validation *store.EvidenceGraphValidationError
	if errors.As(err, &validation) {
		status := http.StatusBadRequest
		switch validation.Code {
		case "PROPOSAL_BLOCKED", "PROPOSAL_CHANGED", "PROJECT_EXISTS", "PRIMARY_MAP_EXISTS", "PRIMARY_MAP_REQUIRED", "GRAPH_PROJECT_MISMATCH", "RUN_PROJECT_MISMATCH", "SNAPSHOT_PROJECT_MISMATCH", "ARCHIVE_REQUIRED", "MAP_HAS_PROPOSALS", "MAP_STILL_REFERENCED":
			status = http.StatusConflict
		case "PROJECT_NOT_REGISTERED", "PROJECT_NOT_FOUND", "GRAPH_NOT_FOUND", "RUN_NOT_FOUND", "SNAPSHOT_NOT_FOUND":
			status = http.StatusNotFound
		case "RUN_PROJECT_REQUIRED", "PROJECT_ID_REQUIRED", "TARGET_MAP_REQUIRED":
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]interface{}{"error": validation.Code, "details": validation.Message})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "evidence graph record not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
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

	if req.Actor == "" {
		req.Actor = "api"
	}

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
	if parseBoolQuery(r.URL.Query().Get("meta")) {
		total, err := s.store.CountExecEvents(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, paginatedResponse[store.ExecEvent]{
			Items:  events,
			Total:  total,
			Limit:  filter.Limit,
			Offset: filter.Offset,
		})
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
	logPath := strings.TrimSpace(r.URL.Query().Get("path"))
	afterLine, _ := strconv.Atoi(r.URL.Query().Get("after_line"))
	if source == "" {
		source = "stdout"
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ws upgrade", "error", err)
		return
	}
	defer conn.Close()
	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	go func() {
		defer cancelStream()
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	if !isFalseQuery(r.URL.Query().Get("snapshot")) {
		var lastLines []executor.LogLine
		if logPath != "" {
			lastLines, _ = s.executor.GetLogFileSnapshot(r.Context(), id, logPath, 200)
		} else {
			lastLines, _ = s.executor.GetLogSnapshot(r.Context(), id, source, 200)
		}
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

	// Stream only new lines; the UI fetches its initial snapshot over HTTP.
	var logCh <-chan executor.LogLine
	var streamErr error
	if logPath != "" {
		logCh, streamErr = s.executor.TailLogFileAfter(streamCtx, id, logPath, afterLine)
	} else {
		logCh, streamErr = s.executor.TailLogsAfter(streamCtx, id, source, afterLine)
	}
	if streamErr != nil {
		conn.WriteJSON(map[string]string{"type": "error", "message": streamErr.Error()})
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
			cancelStream()
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
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
