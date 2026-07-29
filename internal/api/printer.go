package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) requirePrinter(w http.ResponseWriter) bool {
	if s.printer == nil {
		writeError(w, http.StatusServiceUnavailable, "PRINTER_UNAVAILABLE", "printer manager is not configured")
		return false
	}
	return true
}

func (s *Server) handlePrinterStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requirePrinter(w) {
		return
	}
	status, err := s.printer.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PRINTER_STATUS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

type printerConfigRequest struct {
	Enabled bool   `json:"enabled"`
	Queue   string `json:"queue"`
}

func (s *Server) handlePrinterConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requirePrinter(w) {
		return
	}
	var request printerConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PRINTER_CONFIG", err.Error())
		return
	}
	if strings.ContainsAny(request.Queue, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "INVALID_PRINTER_QUEUE", "queue must be a single plain-text value")
		return
	}
	settings, err := s.printer.Configure(r.Context(), request.Enabled, request.Queue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PRINTER_CONFIG_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePrinterTest(w http.ResponseWriter, r *http.Request) {
	if !s.requirePrinter(w) {
		return
	}
	job, err := s.printer.EnqueueTest(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PRINTER_TEST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handlePrinterJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requirePrinter(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.printer.Jobs(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PRINTER_JOBS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": jobs, "total": len(jobs)})
}

func (s *Server) handlePrinterRetry(w http.ResponseWriter, r *http.Request) {
	if !s.requirePrinter(w) {
		return
	}
	job, err := s.printer.Retry(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "PRINT_JOB_NOT_RETRYABLE", "only failed or uncertain print jobs can be retried")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PRINTER_RETRY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
