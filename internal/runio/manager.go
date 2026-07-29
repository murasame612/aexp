package runio

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

// Manager recovers output finalization independently from process lifecycle.
// TransferJob itself resumes payload movement; this reconciler reconnects a
// succeeded Run and its bindings to that durable job after a server restart.
type Manager struct {
	Store    store.Store
	Service  *Service
	Interval time.Duration
	Logger   *slog.Logger

	mu     sync.Mutex
	active map[string]struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewManager(db store.Store, service *Service, interval time.Duration, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{Store: db, Service: service, Interval: interval, Logger: logger, active: map[string]struct{}{}}
}

func (m *Manager) Start() {
	if m.cancel != nil || m.Store == nil || m.Service == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.Interval)
		defer ticker.Stop()
		for {
			m.reconcile(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.wg.Wait()
}

func (m *Manager) reconcile(ctx context.Context) {
	runs, err := m.Store.ListRuns(ctx, store.RunFilter{Status: store.RunStatusSucceeded, Limit: 200})
	if err != nil {
		m.Logger.Warn("list runs for data finalization", "error", err)
		return
	}
	for index := range runs {
		run := runs[index]
		if run.DataFinalizationState != store.RunDataFinalizationPending && run.DataFinalizationState != store.RunDataFinalizationPublishing {
			continue
		}
		m.launch(ctx, run)
	}
}

func (m *Manager) launch(ctx context.Context, run store.Run) {
	m.mu.Lock()
	if _, exists := m.active[run.ID]; exists {
		m.mu.Unlock()
		return
	}
	m.active[run.ID] = struct{}{}
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); delete(m.active, run.ID); m.mu.Unlock() }()
		resource, err := m.Store.GetResource(ctx, run.ResourceID)
		if err != nil || resource == nil {
			m.Logger.Warn("load resource for data finalization", "run_id", run.ID, "error", err)
			return
		}
		if err := m.Service.FinalizeOutputs(ctx, &run, resource); err != nil && ctx.Err() == nil {
			m.Logger.Warn("run data finalization stopped", "run_id", run.ID, "state", run.DataFinalizationState, "error", err)
		}
	}()
}
