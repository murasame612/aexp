package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/store"
)

// RunReconciler keeps cached active-run lifecycle state aligned with the
// remote control plane. It groups work by resource and limits cross-resource
// concurrency so a slow host cannot create an unbounded SSH probe storm.
type RunReconciler struct {
	store        store.Store
	executor     runRefresher
	interval     time.Duration
	probeTimeout time.Duration
	concurrency  int
	logger       *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type runRefresher interface {
	RefreshRunsWithConcurrency(context.Context, []store.Run, time.Duration, int) ([]store.Run, map[string]bool, error)
}

func NewRunReconciler(s store.Store, exec *executor.Executor, interval, probeTimeout time.Duration, concurrency int, logger *slog.Logger) *RunReconciler {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if probeTimeout <= 0 {
		probeTimeout = 3 * time.Second
	}
	if concurrency <= 0 {
		concurrency = 3
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &RunReconciler{store: s, executor: exec, interval: interval, probeTimeout: probeTimeout, concurrency: concurrency, logger: logger, ctx: ctx, cancel: cancel}
}

func (r *RunReconciler) Start() {
	r.wg.Add(1)
	go r.loop()
}

func (r *RunReconciler) Stop() {
	r.cancel()
	r.wg.Wait()
}

func (r *RunReconciler) loop() {
	defer r.wg.Done()
	r.reconcileAndReport(r.ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.reconcileAndReport(r.ctx)
		}
	}
}

func (r *RunReconciler) reconcileAndReport(ctx context.Context) {
	if err := r.reconcile(ctx); err != nil {
		r.logger.Warn("reconcile runs failed", "error", err)
	}
}

func (r *RunReconciler) reconcile(ctx context.Context) error {
	if r.executor == nil || r.store == nil {
		return nil
	}
	var active []store.Run
	for _, status := range []string{store.RunStatusStarting, store.RunStatusRunning, store.RunStatusSSHUnreachable} {
		runs, err := r.store.ListRuns(ctx, store.RunFilter{Status: status})
		if err != nil {
			return fmt.Errorf("list %s runs for reconciliation: %w", status, err)
		}
		active = append(active, runs...)
	}
	if len(active) == 0 {
		return nil
	}
	if _, _, err := r.executor.RefreshRunsWithConcurrency(ctx, active, r.probeTimeout, r.concurrency); err != nil {
		return err
	}
	return nil
}
