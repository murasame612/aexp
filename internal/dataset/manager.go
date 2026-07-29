package dataset

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type Manager struct {
	Store    store.Store
	Service  *Service
	Interval time.Duration
	Logger   *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewManager(db store.Store, service *Service, interval time.Duration, logger *slog.Logger) *Manager {
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{Store: db, Service: service, Interval: interval, Logger: logger}
}

func (m *Manager) Start() {
	if m.cancel != nil {
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
	if ctx.Err() != nil || m.Store == nil || m.Service == nil {
		return
	}
	items, err := m.Store.ListDatasetMaterializations(ctx, "")
	if err != nil {
		m.Logger.Error("list dataset materializations", "error", err)
		return
	}
	for _, item := range items {
		if item.TransferID == "" || (item.State != store.MaterializationTransferring && item.State != store.MaterializationVerifying) {
			continue
		}
		if _, err := m.Service.ReconcileMaterialization(ctx, item.DatasetVersionID, item.ResourceID); err != nil && ctx.Err() == nil {
			m.Logger.Warn("reconcile dataset materialization", "materialization_id", item.ID, "transfer_id", item.TransferID, "error", err)
		}
	}
}
