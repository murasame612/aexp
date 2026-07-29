package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

func (s *Server) handleListLogicalRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := s.store.ListLogicalRoots(r.Context(), strings.TrimSpace(r.URL.Query().Get("workspace")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_LOGICAL_ROOTS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": roots, "total": len(roots)})
}

func (s *Server) handleSaveLogicalRoot(w http.ResponseWriter, r *http.Request) {
	var root store.LogicalRoot
	if err := json.NewDecoder(r.Body).Decode(&root); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_LOGICAL_ROOT", err.Error())
		return
	}
	if id := chi.URLParam(r, "id"); id != "" {
		if root.ID != "" && root.ID != id {
			writeError(w, http.StatusBadRequest, "LOGICAL_ROOT_ID_MISMATCH", "body id must match URL id")
			return
		}
		root.ID = id
	}
	if root.ID == "" {
		root.ID = genID("root_")
	}
	if err := s.store.SaveLogicalRoot(r.Context(), &root); err != nil {
		writeError(w, http.StatusBadRequest, "SAVE_LOGICAL_ROOT_FAILED", err.Error())
		return
	}
	saved, err := s.store.GetLogicalRoot(r.Context(), root.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_LOGICAL_ROOT_FAILED", err.Error())
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

func (s *Server) handleDeleteLogicalRoot(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteLogicalRoot(r.Context(), chi.URLParam(r, "id")); err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeError(w, status, "DELETE_LOGICAL_ROOT_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type logicalPathRequest struct {
	URI        string `json:"uri"`
	ResourceID string `json:"resource_id,omitempty"`
}

func (s *Server) handleResolveLogicalPath(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	var request logicalPathRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PATH_REQUEST", err.Error())
		return
	}
	result, err := s.files.Resolve(r.Context(), request.URI)
	if err != nil {
		writePathError(w, "RESOLVE_PATH_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLocateLogicalPath(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	items, err := s.files.Locate(r.Context(), r.URL.Query().Get("uri"))
	if err != nil {
		writePathError(w, "LOCATE_PATH_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) handleInspectLogicalPath(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	var request logicalPathRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PATH_REQUEST", err.Error())
		return
	}
	result, err := s.files.Inspect(r.Context(), request.URI, request.ResourceID)
	if err != nil {
		writePathError(w, "INSPECT_PATH_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListLogicalPath(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	limit := queryLimit(r, 100, 500)
	result, err := s.files.List(r.Context(), r.URL.Query().Get("uri"), r.URL.Query().Get("resource_id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writePathError(w, "LIST_PATH_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleHashLogicalPath(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	var request logicalPathRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PATH_REQUEST", err.Error())
		return
	}
	result, err := s.files.Hash(r.Context(), request.URI, request.ResourceID)
	if err != nil {
		writePathError(w, "HASH_PATH_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleStorageStat(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	result, err := s.files.StatURI(r.Context(), r.URL.Query().Get("uri"), r.URL.Query().Get("resource_id"))
	if err != nil {
		writePathError(w, "STORAGE_STAT_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleStorageList(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	result, err := s.files.ListURI(r.Context(), r.URL.Query().Get("uri"), r.URL.Query().Get("resource_id"), r.URL.Query().Get("cursor"), queryLimit(r, 50, 500))
	if err != nil {
		writePathError(w, "STORAGE_LIST_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleStorageLocations(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileSpace(w) {
		return
	}
	uri := r.URL.Query().Get("uri")
	locations, err := s.files.LocationsURI(r.Context(), uri)
	if err != nil {
		writePathError(w, "STORAGE_LOCATIONS_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uri": uri, "locations": locations, "total": len(locations)})
}

type storageCopyRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func (s *Server) handleStorageCopy(w http.ResponseWriter, r *http.Request) {
	if s.transfers == nil {
		writeError(w, http.StatusServiceUnavailable, "TRANSFER_SERVICE_UNAVAILABLE", "transfer service is not configured")
		return
	}
	var request storageCopyRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_STORAGE_COPY_REQUEST", err.Error())
		return
	}
	job, created, plan, err := s.transfers.CreateCurrent(r.Context(), transfer.PlanRequest{Source: request.Source, Destination: request.Destination, Initiator: "auto", Verification: "sha256"})
	if err != nil {
		var blocked *transfer.PlanBlockedError
		if errors.As(err, &blocked) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"accepted": false, "state": "blocked", "source": request.Source, "destination": request.Destination, "blockers": blocked.Blockers})
			return
		}
		writePathError(w, "STORAGE_COPY_FAILED", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "transfer_id": job.ID, "state": job.State, "created": created, "source": plan.Source.URI, "destination": plan.Destination.URI, "source_revision": plan.Source.Revision, "total_bytes": plan.TotalBytes, "file_count": plan.FileCount})
}

type transferRequest struct {
	Source             string `json:"source"`
	Destination        string `json:"destination"`
	SourceRevision     string `json:"source_revision,omitempty"`
	Initiator          string `json:"initiator,omitempty"`
	Verification       string `json:"verification,omitempty"`
	ExpectedPlanSHA256 string `json:"expected_plan_sha256,omitempty"`
}

func (r transferRequest) planRequest() transfer.PlanRequest {
	return transfer.PlanRequest{Source: r.Source, Destination: r.Destination, SourceRevision: r.SourceRevision, Initiator: r.Initiator, Verification: r.Verification}
}

func (s *Server) handlePlanTransfer(w http.ResponseWriter, r *http.Request) {
	if s.transferPlanner == nil {
		writeError(w, http.StatusServiceUnavailable, "TRANSFER_PLANNER_UNAVAILABLE", "transfer planner is not configured")
		return
	}
	var request transferRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TRANSFER_REQUEST", err.Error())
		return
	}
	plan, err := s.transferPlanner.Build(r.Context(), request.planRequest())
	if err != nil {
		writeError(w, http.StatusBadRequest, "PLAN_TRANSFER_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleCreateTransfer(w http.ResponseWriter, r *http.Request) {
	if s.transfers == nil {
		writeError(w, http.StatusServiceUnavailable, "TRANSFER_SERVICE_UNAVAILABLE", "transfer service is not configured")
		return
	}
	var request transferRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TRANSFER_REQUEST", err.Error())
		return
	}
	job, created, err := s.transfers.Create(r.Context(), request.planRequest(), request.ExpectedPlanSHA256)
	if err != nil {
		var mismatch *transfer.PlanHashMismatchError
		var blocked *transfer.PlanBlockedError
		switch {
		case errors.As(err, &mismatch):
			writeJSON(w, http.StatusConflict, map[string]any{"error": "TRANSFER_PLAN_CHANGED", "details": err.Error(), "expected_plan_sha256": mismatch.Expected, "actual_plan_sha256": mismatch.Actual})
		case errors.As(err, &blocked):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "TRANSFER_PLAN_BLOCKED", "details": err.Error(), "plan_sha256": blocked.PlanSHA256, "blockers": blocked.Blockers})
		default:
			writeError(w, http.StatusBadRequest, "CREATE_TRANSFER_FAILED", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"transfer": job, "created": created})
}

// handleEnsureLogicalPath is the typed path-level alias for creating a
// TransferJob. It intentionally shares plan hashing, blockers, idempotency,
// routing, verification, and recovery with POST /transfers.
func (s *Server) handleEnsureLogicalPath(w http.ResponseWriter, r *http.Request) {
	s.handleCreateTransfer(w, r)
}

func (s *Server) handleListTransfers(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 100, 500)
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	var updatedSince *time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("updated_since")); raw != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
			updatedSince = &parsed
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_UPDATED_SINCE", parseErr.Error())
			return
		}
	}
	items, err := s.store.ListTransferJobsPage(r.Context(), strings.TrimSpace(r.URL.Query().Get("state")), strings.TrimSpace(r.URL.Query().Get("workspace")), updatedSince, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_TRANSFERS_FAILED", err.Error())
		return
	}
	summaries := make([]map[string]any, 0, len(items))
	for index := range items {
		job := items[index]
		summary := map[string]any{"job": job}
		if record, planErr := s.store.GetTransferPlan(r.Context(), job.PlanSHA256); planErr == nil && record != nil {
			if plan, decodeErr := transfer.DecodePlan(record); decodeErr == nil {
				summary["source"] = plan.Source
				summary["destination"] = plan.Destination
				summary["initiator"] = plan.Initiator
				summary["command_resource_id"] = plan.CommandResourceID
				summary["payload_direction"] = plan.PayloadDirection
				summary["local_data_path"] = plan.LocalDataPath
			}
		}
		summaries = append(summaries, summary)
	}
	nextCursor := 0
	if len(items) == limit {
		nextCursor = offset + len(items)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": summaries, "total": len(summaries), "next_cursor": nextCursor})
}

func (s *Server) handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	if s.transfers == nil {
		writeError(w, http.StatusServiceUnavailable, "TRANSFER_SERVICE_UNAVAILABLE", "transfer service is not configured")
		return
	}
	detail, err := s.transfers.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GET_TRANSFER_FAILED", err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "TRANSFER_NOT_FOUND", "transfer was not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleRetryTransfer(w http.ResponseWriter, r *http.Request) {
	if s.transfers == nil {
		writeError(w, http.StatusServiceUnavailable, "TRANSFER_SERVICE_UNAVAILABLE", "transfer service is not configured")
		return
	}
	job, err := s.transfers.Retry(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusConflict, "RETRY_TRANSFER_FAILED", err.Error())
		return
	}
	if job == nil {
		writeError(w, http.StatusNotFound, "TRANSFER_NOT_FOUND", "transfer was not found")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleCancelTransfer(w http.ResponseWriter, r *http.Request) {
	if s.transfers == nil {
		writeError(w, http.StatusServiceUnavailable, "TRANSFER_SERVICE_UNAVAILABLE", "transfer service is not configured")
		return
	}
	job, err := s.transfers.Cancel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusConflict, "CANCEL_TRANSFER_FAILED", err.Error())
		return
	}
	if job == nil {
		writeError(w, http.StatusNotFound, "TRANSFER_NOT_FOUND", "transfer was not found")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) requireFileSpace(w http.ResponseWriter) bool {
	if s.files != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "FILE_SPACE_UNAVAILABLE", "file-space service is not configured")
	return false
}

func writePathError(w http.ResponseWriter, code string, err error) {
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no logical root") {
		status = http.StatusNotFound
	}
	var remoteErr *filespace.RemoteError
	if errors.As(err, &remoteErr) {
		status = http.StatusBadGateway
	}
	writeError(w, status, code, err.Error())
}

func queryLimit(r *http.Request, fallback, maximum int) int {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > maximum {
		return maximum
	}
	return limit
}
