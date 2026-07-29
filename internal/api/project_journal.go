package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ziwu/aexp/internal/store"
)

func projectJournalFilterFromRequest(r *http.Request) store.ProjectJournalFilter {
	filter := store.ProjectJournalFilter{
		RunID:            strings.TrimSpace(r.URL.Query().Get("run_id")),
		Query:            strings.TrimSpace(r.URL.Query().Get("query")),
		NextActionStatus: strings.TrimSpace(r.URL.Query().Get("next_action_status")),
	}
	if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && limit > 0 {
		if limit > 200 {
			limit = 200
		}
		filter.Limit = limit
	}
	if offset, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && offset > 0 {
		filter.Offset = offset
	}
	return filter
}

func writeProjectJournalError(w http.ResponseWriter, err error) {
	var validation *store.EvidenceGraphValidationError
	if errors.As(err, &validation) {
		status := http.StatusBadRequest
		switch validation.Code {
		case "PROJECT_NOT_FOUND", "RUN_NOT_FOUND":
			status = http.StatusNotFound
		case "RUN_PROJECT_MISMATCH":
			status = http.StatusConflict
		}
		writeError(w, status, validation.Code, validation.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
}

func (s *Server) handleListProjectJournal(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	project, err := s.store.GetProjectDefinition(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if project == nil {
		writeError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
		return
	}
	filter := projectJournalFilterFromRequest(r)
	filter.ProjectID = projectID
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	entries, err := s.store.ListProjectJournalEntries(r.Context(), filter)
	if err != nil {
		writeProjectJournalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleListProjectJournalForRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "RUN_NOT_FOUND", "run not found")
		return
	}
	filter := projectJournalFilterFromRequest(r)
	filter.RunID = runID
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	entries, err := s.store.ListProjectJournalEntries(r.Context(), filter)
	if err != nil {
		writeProjectJournalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleGetProjectJournalEntry(w http.ResponseWriter, r *http.Request) {
	entry, err := s.store.GetProjectJournalEntry(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeProjectJournalError(w, err)
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "JOURNAL_NOT_FOUND", "journal entry not found")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleCreateProjectJournalEntry(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	var entry store.ProjectJournalEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	entry.ID = genID("journal_")
	entry.ProjectID = projectID
	if err := s.store.CreateProjectJournalEntry(r.Context(), &entry); err != nil {
		writeProjectJournalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleUpdateProjectJournalNextAction(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	entry, err := s.store.UpdateProjectJournalNextActionStatus(
		r.Context(),
		chi.URLParam(r, "id"),
		request.Status,
	)
	if err != nil {
		writeProjectJournalError(w, err)
		return
	}
	if entry == nil {
		writeError(w, http.StatusNotFound, "JOURNAL_NOT_FOUND", "journal entry not found")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}
