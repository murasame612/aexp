package transfer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

// Manager is the persistent transfer reconciler. Jobs are database state, not
// goroutines: after a control-plane restart it resumes staging verification or
// promotion from the last durable stage.
type Manager struct {
	Store       store.Store
	Worker      *Worker
	Interval    time.Duration
	ResumeAfter time.Duration
	Concurrency int
	Logger      *slog.Logger
	Now         func() time.Time

	mu     sync.Mutex
	active map[string]struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}
}

func NewManager(db store.Store, worker *Worker, interval time.Duration, concurrency int, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = time.Second
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{Store: db, Worker: worker, Interval: interval, ResumeAfter: 30 * time.Second, Concurrency: concurrency, Logger: logger, Now: time.Now, active: make(map[string]struct{}), sem: make(chan struct{}, concurrency)}
}

func (m *Manager) Start() error {
	if m.Store == nil || m.Worker == nil {
		return fmt.Errorf("transfer store and worker are required")
	}
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()
	m.wg.Add(1)
	go m.loop(ctx)
	return nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *Manager) loop(ctx context.Context) {
	defer m.wg.Done()
	m.reconcile(ctx)
	ticker := time.NewTicker(m.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	states := []string{store.TransferCancelling, store.TransferPromoting, store.TransferVerifying, store.TransferTransferring, store.TransferPlanning, store.TransferQueued}
	for _, state := range states {
		if ctx.Err() != nil {
			return
		}
		jobs, err := m.Store.ListTransferJobs(ctx, state, 100)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.Logger.Error("list transfer jobs for reconciliation", "state", state, "error", err)
			continue
		}
		for index := range jobs {
			if state != store.TransferQueued && state != store.TransferCancelling && m.hasFreshLease(jobs[index]) {
				continue
			}
			m.launch(ctx, jobs[index].ID, state != store.TransferQueued)
		}
	}
}

func (m *Manager) hasFreshLease(job store.TransferJob) bool {
	if m.ResumeAfter <= 0 {
		return false
	}
	reference := job.UpdatedAt
	if job.HeartbeatAt != nil {
		reference = *job.HeartbeatAt
	} else if reference.IsZero() && job.StartedAt != nil {
		reference = *job.StartedAt
	}
	if reference.IsZero() {
		return false
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	return now.UTC().Sub(reference.UTC()) < m.ResumeAfter
}

func (m *Manager) launch(ctx context.Context, id string, resume bool) {
	m.mu.Lock()
	if _, exists := m.active[id]; exists {
		m.mu.Unlock()
		return
	}
	m.active[id] = struct{}{}
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		select {
		case m.sem <- struct{}{}:
		case <-ctx.Done():
			m.release(id)
			return
		}
		defer func() {
			<-m.sem
			m.release(id)
		}()
		var err error
		if resume {
			err = m.Worker.Resume(ctx, id)
		} else {
			err = m.Worker.Execute(ctx, id)
		}
		if err != nil && ctx.Err() == nil {
			m.Logger.Warn("transfer worker stopped", "transfer_id", id, "error", err)
		}
	}()
}

func (m *Manager) release(id string) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}
