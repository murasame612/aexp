package transfer

import (
	"context"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestManagerExecutesQueuedJobs(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	manager := NewManager(db, worker, 5*time.Millisecond, 1, nil)
	manager.ResumeAfter = time.Nanosecond
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	waitForTransferState(t, db, id, store.TransferCompleted)
	if transport.copyCalls != 1 || transport.verifyCalls != 1 || transport.promoteCalls != 1 {
		t.Fatalf("transport=%#v", transport)
	}
}

func TestManagerResumesDurableVerifyingStage(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	job, claimed, err := db.ClaimTransferJob(context.Background(), id)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	job.State, job.Stage = store.TransferTransferring, store.TransferTransferring
	if updated, err := db.UpdateTransferJobIfState(context.Background(), job, store.TransferPlanning); err != nil || !updated {
		t.Fatalf("transferring=%v err=%v", updated, err)
	}
	job.State, job.Stage = store.TransferVerifying, store.TransferVerifying
	if updated, err := db.UpdateTransferJobIfState(context.Background(), job, store.TransferTransferring); err != nil || !updated {
		t.Fatalf("verifying=%v err=%v", updated, err)
	}
	manager := NewManager(db, worker, 5*time.Millisecond, 1, nil)
	manager.ResumeAfter = time.Nanosecond
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	waitForTransferState(t, db, id, store.TransferCompleted)
	if transport.copyCalls != 0 || transport.verifyCalls != 1 || transport.promoteCalls != 1 {
		t.Fatalf("transport=%#v", transport)
	}
}

func TestManagerResumesDurableTransferringStage(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	advanceTransferTo(t, db, id, store.TransferTransferring)
	if err := db.SaveTransferAttempt(context.Background(), &store.TransferAttempt{ID: "attempt_" + id + "_1", TransferID: id, Number: 1, Initiator: "nas", State: store.TransferTransferring, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, worker, 5*time.Millisecond, 1, nil)
	manager.ResumeAfter = time.Nanosecond
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	waitForTransferState(t, db, id, store.TransferCompleted)
	if transport.copyCalls != 1 || transport.verifyCalls != 1 || transport.promoteCalls != 1 {
		t.Fatalf("transport=%#v", transport)
	}
	attempts, err := db.ListTransferAttempts(context.Background(), id)
	if err != nil || len(attempts) != 2 || attempts[0].State != store.TransferFailed || attempts[0].ErrorCode != "worker_interrupted" || attempts[1].State != store.TransferCompleted {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}

func TestManagerResumesDurablePromotingStageWithoutRecopyOrRehash(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	advanceTransferTo(t, db, id, store.TransferPromoting)
	manager := NewManager(db, worker, 5*time.Millisecond, 1, nil)
	manager.ResumeAfter = time.Nanosecond
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	waitForTransferState(t, db, id, store.TransferCompleted)
	if transport.copyCalls != 0 || transport.verifyCalls != 0 || transport.promoteCalls != 1 {
		t.Fatalf("transport=%#v", transport)
	}
}

func TestManagerDoesNotStealFreshTransferLease(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	advanceTransferTo(t, db, id, store.TransferTransferring)
	manager := NewManager(db, worker, 5*time.Millisecond, 1, nil)
	manager.ResumeAfter = time.Hour
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	manager.Stop()
	job, err := db.GetTransferJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.State != store.TransferTransferring {
		t.Fatalf("fresh transfer lease was stolen: %#v", job)
	}
	if transport.copyCalls != 0 || transport.verifyCalls != 0 || transport.promoteCalls != 0 {
		t.Fatalf("fresh transfer was resumed: %#v", transport)
	}
}

func advanceTransferTo(t *testing.T, db store.Store, id, target string) {
	t.Helper()
	job, claimed, err := db.ClaimTransferJob(context.Background(), id)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	states := []string{store.TransferTransferring, store.TransferVerifying, store.TransferPromoting}
	expected := store.TransferPlanning
	for _, state := range states {
		job.State, job.Stage = state, state
		if updated, err := db.UpdateTransferJobIfState(context.Background(), job, expected); err != nil || !updated {
			t.Fatalf("advance to %s updated=%v err=%v", state, updated, err)
		}
		if state == target {
			return
		}
		expected = state
	}
	t.Fatalf("unsupported target state %s", target)
}

func waitForTransferState(t *testing.T, db store.Store, id, state string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := db.GetTransferJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job != nil && job.State == state {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := db.GetTransferJob(context.Background(), id)
	t.Fatalf("transfer did not reach %s: %#v", state, job)
}
