package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *SQLite) GetPrinterSettings(ctx context.Context) (*PrinterSettings, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO printer_settings(id, last_event_seq)
		VALUES (1, (SELECT COALESCE(MAX(seq), 0) FROM printer_run_events))`); err != nil {
		return nil, err
	}
	settings := &PrinterSettings{}
	err := s.db.QueryRowContext(ctx, `SELECT enabled, queue, last_event_seq, enabled_from_event_seq, created_at, updated_at
		FROM printer_settings WHERE id=1`).Scan(&settings.Enabled, &settings.Queue, &settings.LastEventSeq, &settings.EnabledFromEventSeq, &settings.CreatedAt, &settings.UpdatedAt)
	return settings, err
}

func (s *SQLite) PrinterRunEligible(ctx context.Context, runID string, enabledFromEventSeq int64) (bool, error) {
	var eligible bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM printer_run_events WHERE run_id=? AND operation='insert' AND seq>?
	)`, runID, enabledFromEventSeq).Scan(&eligible)
	return eligible, err
}

func (s *SQLite) ListPrinterRunEvents(ctx context.Context, afterSeq int64, limit int) ([]PrinterRunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, run_id, operation, status, kind, started_at, finished_at, changed_at
		FROM printer_run_events WHERE seq>? ORDER BY seq LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]PrinterRunEvent, 0)
	for rows.Next() {
		var event PrinterRunEvent
		if err := rows.Scan(&event.Seq, &event.RunID, &event.Operation, &event.Status, &event.Kind, &event.StartedAt, &event.FinishedAt, &event.ChangedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLite) ConfigurePrinter(ctx context.Context, enabled bool, queue string) (*PrinterSettings, error) {
	queue = strings.TrimSpace(queue)
	if queue == "" {
		queue = "Printer_POS_80"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO printer_settings(id, last_event_seq)
		VALUES (1, (SELECT COALESCE(MAX(seq), 0) FROM printer_run_events))`); err != nil {
		return nil, err
	}
	var wasEnabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM printer_settings WHERE id=1`).Scan(&wasEnabled); err != nil {
		return nil, err
	}
	if enabled && !wasEnabled {
		_, err = tx.ExecContext(ctx, `UPDATE printer_settings SET enabled=1, queue=?,
			last_event_seq=(SELECT COALESCE(MAX(seq), 0) FROM printer_run_events),
			enabled_from_event_seq=(SELECT COALESCE(MAX(seq), 0) FROM printer_run_events),
			updated_at=CURRENT_TIMESTAMP WHERE id=1`, queue)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE printer_settings SET enabled=?, queue=?, updated_at=CURRENT_TIMESTAMP WHERE id=1`, enabled, queue)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPrinterSettings(ctx)
}

func insertPrintJob(ctx context.Context, tx *sql.Tx, job PrintJob) error {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.Queue) == "" || strings.TrimSpace(job.ReceiptText) == "" {
		return fmt.Errorf("print job requires id, queue and receipt_text")
	}
	if job.State == "" {
		job.State = PrintJobQueued
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO run_print_jobs
		(id, run_id, phase, event_seq, state, queue, title, receipt_text, cups_job_id, attempts, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, job.ID, job.RunID, job.Phase, job.EventSeq, job.State, job.Queue, job.Title, job.ReceiptText, job.CUPSJobID, job.Attempts, job.LastError)
	return err
}

func (s *SQLite) EnqueuePrintJobsAndAdvanceCursor(ctx context.Context, expectedCursor, nextCursor int64, jobs []PrintJob) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var enabled bool
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT enabled, last_event_seq FROM printer_settings WHERE id=1`).Scan(&enabled, &cursor); err != nil {
		return false, err
	}
	if !enabled || cursor != expectedCursor {
		return false, nil
	}
	for _, job := range jobs {
		if err = insertPrintJob(ctx, tx, job); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE printer_settings SET last_event_seq=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=1 AND enabled=1 AND last_event_seq=?`, nextCursor, expectedCursor)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLite) EnqueuePrintJob(ctx context.Context, job *PrintJob) error {
	if job == nil {
		return fmt.Errorf("print job is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertPrintJob(ctx, tx, *job); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return scanPrintJob(s.db.QueryRowContext(ctx, `SELECT `+printJobColumns+` FROM run_print_jobs WHERE id=?`, job.ID), job)
}

func scanPrintJob(row rowScanner, job *PrintJob) error {
	return row.Scan(&job.Ordinal, &job.ID, &job.RunID, &job.Phase, &job.EventSeq, &job.State, &job.Queue,
		&job.Title, &job.ReceiptText, &job.CUPSJobID, &job.Attempts, &job.LastError, &job.CreatedAt,
		&job.UpdatedAt, &job.StartedAt, &job.FinishedAt)
}

const printJobColumns = `ordinal, id, run_id, phase, event_seq, state, queue, title, receipt_text,
	cups_job_id, attempts, last_error, created_at, updated_at, started_at, finished_at`

func (s *SQLite) ClaimNextPrintJob(ctx context.Context) (*PrintJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM run_print_jobs WHERE state=? ORDER BY ordinal LIMIT 1`, PrintJobQueued).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_print_jobs SET state=?, attempts=attempts+1,
		started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND state=?`, PrintJobSubmitting, id, PrintJobQueued)
	if err != nil {
		return nil, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, false, nil
	}
	job := &PrintJob{}
	if err = scanPrintJob(tx.QueryRowContext(ctx, `SELECT `+printJobColumns+` FROM run_print_jobs WHERE id=?`, id), job); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return job, true, nil
}

func (s *SQLite) CompletePrintJob(ctx context.Context, id, cupsJobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run_print_jobs SET state=?, cups_job_id=?, last_error='',
		finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND state=?`, PrintJobSpooled, cupsJobID, id, PrintJobSubmitting)
	return err
}

func (s *SQLite) FailPrintJob(ctx context.Context, id, state, lastError string) error {
	if state != PrintJobFailed && state != PrintJobUncertain {
		return fmt.Errorf("invalid print failure state %q", state)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE run_print_jobs SET state=?, last_error=?,
		finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`, state, lastError, id)
	return err
}

func (s *SQLite) RecoverSubmittingPrintJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run_print_jobs SET state=?,
		last_error='service restarted while CUPS submission outcome was unknown', updated_at=CURRENT_TIMESTAMP
		WHERE state=?`, PrintJobUncertain, PrintJobSubmitting)
	return err
}

func (s *SQLite) ListPrintJobs(ctx context.Context, limit int) ([]PrintJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+printJobColumns+` FROM run_print_jobs ORDER BY ordinal DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]PrintJob, 0)
	for rows.Next() {
		var job PrintJob
		if err := scanPrintJob(rows, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLite) RetryPrintJob(ctx context.Context, id string) (*PrintJob, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE run_print_jobs SET state=?, cups_job_id='', last_error='',
		started_at=NULL, finished_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND state IN (?, ?)`,
		PrintJobQueued, id, PrintJobFailed, PrintJobUncertain)
	if err != nil {
		return nil, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, sql.ErrNoRows
	}
	job := &PrintJob{}
	err = scanPrintJob(s.db.QueryRowContext(ctx, `SELECT `+printJobColumns+` FROM run_print_jobs WHERE id=?`, id), job)
	return job, err
}
