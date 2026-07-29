package printer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type Manager struct {
	db       store.Store
	device   Device
	interval time.Duration
	logger   *slog.Logger

	mu          sync.RWMutex
	queueStatus QueueStatus
	lastError   string
	lastChecked *time.Time
	cancel      context.CancelFunc
	done        chan struct{}
}

type Status struct {
	*store.PrinterSettings
	Available     bool       `json:"available"`
	QueueState    string     `json:"queue_state"`
	WorkerState   string     `json:"worker_state"`
	LastError     string     `json:"last_error,omitempty"`
	LastChecked   *time.Time `json:"last_checked_at,omitempty"`
	QueuedJobs    int        `json:"queued_jobs"`
	FailedJobs    int        `json:"failed_jobs"`
	UncertainJobs int        `json:"uncertain_jobs"`
}

func NewManager(db store.Store, device Device, interval time.Duration, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{db: db, device: device, interval: interval, logger: logger, done: make(chan struct{})}
}

func (m *Manager) Start() error {
	if err := m.db.RecoverSubmittingPrintJobs(context.Background()); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.loop(ctx)
	return nil
}

func (m *Manager) Stop() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
}

func (m *Manager) loop(ctx context.Context) {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	healthTicker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer healthTicker.Stop()
	m.refreshQueueStatus(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.SyncOnce(ctx); err != nil {
				m.recordError(err)
				m.logger.Error("printer manager tick", "error", err)
			}
		case <-healthTicker.C:
			m.refreshQueueStatus(ctx)
		}
	}
}

func lifecycleJobID(runID, phase string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + phase))
	return "print_" + hex.EncodeToString(sum[:10])
}

func shouldPrintKind(kind string) bool { return kind != store.RunKindSetup }

func eventPhases(event store.PrinterRunEvent) []string {
	if event.Operation == "delete" || !shouldPrintKind(event.Kind) {
		return nil
	}
	if store.IsRunTerminalStatus(event.Status) {
		if event.StartedAt != nil {
			return []string{store.PrintPhaseStart, store.PrintPhaseEnd}
		}
		return []string{store.PrintPhaseEnd}
	}
	if event.StartedAt != nil && (event.Status == store.RunStatusRunning || event.Status == store.RunStatusSSHUnreachable) {
		return []string{store.PrintPhaseStart}
	}
	return nil
}

func (m *Manager) jobsForEvent(ctx context.Context, event store.PrinterRunEvent, queue string, enabledFromEventSeq int64) ([]store.PrintJob, error) {
	phases := eventPhases(event)
	if len(phases) == 0 {
		return nil, nil
	}
	run, err := m.db.GetRun(ctx, event.RunID)
	if err != nil || run == nil {
		return nil, err
	}
	// A cursor alone cannot exclude an old run that changes state after first
	// enable. Enrol only runs whose insert event belongs to this enabled epoch.
	eligible, err := m.db.PrinterRunEligible(ctx, run.ID, enabledFromEventSeq)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, nil
	}
	resourceName := run.ResourceID
	if resource, resourceErr := m.db.GetResource(ctx, run.ResourceID); resourceErr == nil && resource != nil {
		resourceName = resource.Name
	}
	jobs := make([]store.PrintJob, 0, len(phases))
	for _, phase := range phases {
		title, receipt := BuildReceipt(run, resourceName, phase, event)
		jobs = append(jobs, store.PrintJob{ID: lifecycleJobID(run.ID, phase), RunID: run.ID, Phase: phase,
			EventSeq: event.Seq, State: store.PrintJobQueued, Queue: queue, Title: title, ReceiptText: receipt})
	}
	return jobs, nil
}

func (m *Manager) SyncOnce(ctx context.Context) error {
	settings, err := m.db.GetPrinterSettings(ctx)
	if err != nil {
		return err
	}
	if settings.Enabled {
		for {
			events, err := m.db.ListPrinterRunEvents(ctx, settings.LastEventSeq, 200)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				break
			}
			for _, event := range events {
				jobs, err := m.jobsForEvent(ctx, event, settings.Queue, settings.EnabledFromEventSeq)
				if err != nil {
					return fmt.Errorf("build receipt for event %d: %w", event.Seq, err)
				}
				advanced, err := m.db.EnqueuePrintJobsAndAdvanceCursor(ctx, settings.LastEventSeq, event.Seq, jobs)
				if err != nil {
					return err
				}
				if !advanced {
					return nil
				}
				settings.LastEventSeq = event.Seq
			}
			if len(events) < 200 {
				break
			}
		}
	}
	for {
		job, claimed, err := m.db.ClaimNextPrintJob(ctx)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
		printCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cupsID, printErr := m.device.Print(printCtx, job.Queue, job.Title, job.ReceiptText)
		cancel()
		if printErr != nil {
			if err := m.db.FailPrintJob(ctx, job.ID, store.PrintJobFailed, printErr.Error()); err != nil {
				return err
			}
			m.recordError(printErr)
			continue
		}
		if err := m.db.CompletePrintJob(ctx, job.ID, cupsID); err != nil {
			// CUPS may already own the paper job. Preserve uncertainty rather
			// than silently retrying and producing a duplicate receipt.
			_ = m.db.FailPrintJob(context.Background(), job.ID, store.PrintJobUncertain, "CUPS accepted the job but persistence failed: "+err.Error())
			return err
		}
	}
}

func (m *Manager) Configure(ctx context.Context, enabled bool, queue string) (*store.PrinterSettings, error) {
	return m.db.ConfigurePrinter(ctx, enabled, queue)
}

func (m *Manager) EnqueueTest(ctx context.Context) (*store.PrintJob, error) {
	settings, err := m.db.GetPrinterSettings(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	title, receipt := BuildTestReceipt(now)
	job := &store.PrintJob{ID: fmt.Sprintf("print_test_%d", now.UnixNano()), Phase: store.PrintPhaseTest,
		State: store.PrintJobQueued, Queue: settings.Queue, Title: title, ReceiptText: receipt}
	if err := m.db.EnqueuePrintJob(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func (m *Manager) Jobs(ctx context.Context, limit int) ([]store.PrintJob, error) {
	return m.db.ListPrintJobs(ctx, limit)
}

func (m *Manager) Retry(ctx context.Context, id string) (*store.PrintJob, error) {
	return m.db.RetryPrintJob(ctx, id)
}

func (m *Manager) Status(ctx context.Context) (*Status, error) {
	settings, err := m.db.GetPrinterSettings(ctx)
	if err != nil {
		return nil, err
	}
	jobs, err := m.db.ListPrintJobs(ctx, 200)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	status := &Status{PrinterSettings: settings, Available: m.queueStatus.Available, QueueState: m.queueStatus.State,
		WorkerState: "running", LastError: m.lastError, LastChecked: m.lastChecked}
	m.mu.RUnlock()
	for _, job := range jobs {
		switch job.State {
		case store.PrintJobQueued, store.PrintJobSubmitting:
			status.QueuedJobs++
		case store.PrintJobFailed:
			status.FailedJobs++
		case store.PrintJobUncertain:
			status.UncertainJobs++
		}
	}
	return status, nil
}

func (m *Manager) refreshQueueStatus(ctx context.Context) {
	settings, err := m.db.GetPrinterSettings(ctx)
	if err != nil {
		m.recordError(err)
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	status, err := m.device.Status(checkCtx, settings.Queue)
	cancel()
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastChecked = &now
	if err != nil {
		m.queueStatus = QueueStatus{Available: false, State: "unknown"}
		m.lastError = err.Error()
		return
	}
	m.queueStatus = status
	if status.Available {
		m.lastError = ""
	} else {
		m.lastError = status.Detail
	}
}

func (m *Manager) recordError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.lastError = err.Error()
	m.mu.Unlock()
}
