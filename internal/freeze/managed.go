package freeze

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

type ManagedRuntime struct {
	Store     store.Store
	Planner   *transfer.Planner
	Transfers *transfer.Service
	Worker    *transfer.Worker
	Writer    filespace.MetadataWriter
}

// ExecuteManaged keeps Freeze responsible for scientific selection,
// aggregation, and release gates while all payload movement is delegated to
// the persistent TransferJob engine.
func ExecuteManaged(ctx context.Context, runtime ManagedRuntime, freezeID string) error {
	record, err := runtime.Store.GetRunFreeze(ctx, freezeID)
	if err != nil || record == nil {
		if err == nil {
			err = fmt.Errorf("freeze %s not found", freezeID)
		}
		return err
	}
	if record.State == store.RunFreezeReleased || record.State == store.RunFreezeBlocked {
		return nil
	}
	var persisted PersistedPlan
	if err := json.Unmarshal([]byte(record.ProvenanceJSON), &persisted); err != nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "invalid_plan", err)
	}
	plan := persisted.Plan
	if !plan.Eligible {
		return failFreeze(ctx, runtime.Store, record, record.State, "plan_blocked", fmt.Errorf("freeze plan is not eligible"))
	}
	run, err := runtime.Store.GetRun(ctx, record.RunID)
	if err != nil || run == nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "run_missing", firstErr(err, fmt.Errorf("run missing")))
	}
	compute, err := runtime.Store.GetResource(ctx, run.ResourceID)
	if err != nil || compute == nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "resource_missing", firstErr(err, fmt.Errorf("compute resource missing")))
	}
	target, err := runtime.Store.GetStorageTargetByName(ctx, plan.Profile.Storage)
	if err != nil || target == nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "storage_missing", firstErr(err, fmt.Errorf("storage target %s missing", plan.Profile.Storage)))
	}
	nas, err := runtime.Store.GetResource(ctx, target.ResourceID)
	if err != nil || nas == nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "storage_resource_missing", firstErr(err, fmt.Errorf("storage resource missing")))
	}
	prefix, err := storagePrefix(plan.DestinationURI, target.Name)
	if err != nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "invalid_destination", err)
	}
	root := firstNonBlank(run.ResolvedCwd, run.Cwd)
	if root == "" {
		return failFreeze(ctx, runtime.Store, record, record.State, "source_root_missing", fmt.Errorf("run source root missing"))
	}
	sourceURI, err := resourcePathURI(compute, root)
	if err != nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "source_root_invalid", err)
	}
	rawDestinationURI := (&url.URL{Scheme: "storage", Host: target.Name, Path: "/" + path.Join(prefix, run.ID, record.ID, "raw")}).String()
	selection := make([]transfer.ManifestEntry, 0, len(plan.Files))
	files := make([]store.RunFreezeFile, 0, len(plan.Files))
	for _, file := range plan.Files {
		selection = append(selection, transfer.ManifestEntry{Path: file.RelativePath, Type: "file", SHA256: file.SHA256, Size: file.Size})
		files = append(files, store.RunFreezeFile{
			ID: fileID(record.ID, "raw", file.RelativePath), FreezeID: record.ID, Kind: "raw", Role: file.Role,
			RelativePath: file.RelativePath, SourceURI: file.SourceURI,
			FrozenURI: rawDestinationURI + "/" + strings.TrimPrefix(filepath.ToSlash(file.RelativePath), "/"),
			SHA256:    file.SHA256, Size: file.Size, Required: file.Required, SourceArtifactID: file.ArtifactID,
		})
	}
	normalized, payloadRevision, totalBytes, fileCount, err := transfer.NormalizeSelection(selection)
	if err != nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "invalid_artifact_selection", err)
	}
	if record.State == store.RunFreezeQueued {
		if err := transition(ctx, runtime.Store, record, store.RunFreezeQueued, store.RunFreezeCollecting); err != nil {
			return err
		}
	}
	request := transfer.PlanRequest{
		Source: sourceURI, Destination: rawDestinationURI, SourceRevision: payloadRevision,
		Initiator: "auto", Verification: "manifest", Selection: normalized,
	}
	transferPlan, err := runtime.Planner.Build(ctx, request)
	if err != nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "transfer_plan_failed", err)
	}
	if len(transferPlan.Blockers) > 0 {
		return failFreeze(ctx, runtime.Store, record, record.State, "transfer_plan_blocked", fmt.Errorf("raw transfer has %d blocker(s)", len(transferPlan.Blockers)))
	}
	job, _, err := runtime.Transfers.Create(ctx, request, transferPlan.PlanSHA256)
	if err != nil {
		return failFreeze(ctx, runtime.Store, record, record.State, "transfer_create_failed", err)
	}
	record.RawTransferID, record.FileCount, record.TotalBytes = job.ID, fileCount, totalBytes
	if record.State == store.RunFreezeCollecting {
		if err := transition(ctx, runtime.Store, record, store.RunFreezeCollecting, store.RunFreezeTransferring); err != nil {
			return err
		}
	} else if _, err := runtime.Store.UpdateRunFreezeIfState(ctx, record, record.State); err != nil {
		return err
	}
	job, err = runManagedTransfer(ctx, runtime, record, job.ID)
	if err != nil {
		return failFreeze(ctx, runtime.Store, record, store.RunFreezeTransferring, "raw_transfer_failed", err)
	}
	record.FilesDone, record.BytesDone = job.FilesDone, job.BytesDone
	if record.State == store.RunFreezeTransferring {
		if err := transition(ctx, runtime.Store, record, store.RunFreezeTransferring, store.RunFreezeVerifying); err != nil {
			return err
		}
	}
	rawManifest := map[string]any{
		"schema_version": 2, "freeze_id": record.ID, "run_id": run.ID,
		"payload_revision": payloadRevision, "transfer_id": job.ID, "files": files,
	}
	rawManifestBytes, _ := json.Marshal(rawManifest)
	rawDigest := sha256.Sum256(rawManifestBytes)
	record.RawManifestSHA256 = "sha256:" + hex.EncodeToString(rawDigest[:])
	manifestRelative := path.Join(prefix, run.ID, record.ID, "raw-manifest.json")
	record.ManifestURI = (&url.URL{Scheme: "storage", Host: target.Name, Path: "/" + manifestRelative}).String()
	metadataBytes, _ := json.Marshal(map[string]any{
		"schema_version": 2, "freeze_id": record.ID, "run_id": run.ID, "profile": record.Profile,
		"plan_sha256": record.PlanSHA256, "run_manifest_sha256": record.RunManifestSHA256,
		"raw_manifest_sha256": record.RawManifestSHA256, "payload_revision": payloadRevision,
		"raw_transfer_id": job.ID, "provenance": plan.Provenance,
	})
	if record.State == store.RunFreezeVerifying {
		if runtime.Writer == nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeVerifying, "metadata_writer_unavailable", fmt.Errorf("remote metadata writer is unavailable"))
		}
		for physical, payload := range map[string][]byte{
			path.Join(target.RootPath, manifestRelative):                         rawManifestBytes,
			path.Join(target.RootPath, prefix, run.ID, record.ID, "freeze.json"): metadataBytes,
		} {
			if err := runtime.Writer.WriteAtomic(ctx, filespace.RemoteLocation{Resource: nas, PhysicalPath: physical, Boundary: target.RootPath}, payload, 0o644); err != nil {
				return failFreeze(ctx, runtime.Store, record, store.RunFreezeVerifying, "metadata_write_failed", err)
			}
		}
		if err := runtime.Store.ReplaceRunFreezeFiles(ctx, record.ID, files); err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeVerifying, "freeze_ledger_failed", err)
		}
		frozenAt := time.Now().UTC()
		record.FrozenAt = &frozenAt
		if err := transition(ctx, runtime.Store, record, store.RunFreezeVerifying, store.RunFreezeFrozen); err != nil {
			return err
		}
	}
	if record.WorkspacePath == "" {
		return nil
	}
	workspace := filepath.Join(record.WorkspacePath, run.ID, record.ID)
	workspaceSelection := make([]transfer.ManifestEntry, 0)
	workspaceRoles := make(map[string]bool, len(plan.Profile.WorkspaceRoles))
	for _, role := range plan.Profile.WorkspaceRoles {
		workspaceRoles[role] = true
	}
	for _, file := range plan.Files {
		if workspaceRoles[file.Role] {
			workspaceSelection = append(workspaceSelection, transfer.ManifestEntry{Path: file.RelativePath, Type: "file", SHA256: file.SHA256, Size: file.Size})
		}
	}
	if record.State == store.RunFreezeFrozen && len(workspaceSelection) > 0 {
		normalizedWorkspace, workspaceRevision, _, _, err := transfer.NormalizeSelection(workspaceSelection)
		if err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeFrozen, "workspace_selection_invalid", err)
		}
		workspaceRequest := transfer.PlanRequest{
			Source: rawDestinationURI, Destination: (&url.URL{Scheme: "local", Path: filepath.Join(workspace, "raw")}).String(),
			SourceRevision: workspaceRevision, Initiator: "mac", Verification: "manifest", Selection: normalizedWorkspace,
		}
		workspacePlan, err := runtime.Planner.Build(ctx, workspaceRequest)
		if err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeFrozen, "workspace_plan_failed", err)
		}
		workspaceJob, _, err := runtime.Transfers.Create(ctx, workspaceRequest, workspacePlan.PlanSHA256)
		if err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeFrozen, "workspace_transfer_create_failed", err)
		}
		record.WorkspaceTransferID = workspaceJob.ID
		if _, err := runtime.Store.UpdateRunFreezeIfState(ctx, record, store.RunFreezeFrozen); err != nil {
			return err
		}
		if _, err := runManagedTransfer(ctx, runtime, record, workspaceJob.ID); err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeFrozen, "workspace_materialize_failed", err)
		}
	} else if record.State == store.RunFreezeFrozen {
		if err := os.MkdirAll(filepath.Join(workspace, "raw"), 0o755); err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeFrozen, "workspace_failed", err)
		}
	}
	if record.State == store.RunFreezeFrozen {
		if err := writeWorkspaceMetadata(workspace, map[string][]byte{
			"raw-manifest.json": rawManifestBytes,
			"freeze.json":       metadataBytes,
		}); err != nil {
			return failFreeze(ctx, runtime.Store, record, store.RunFreezeFrozen, "workspace_metadata_failed", err)
		}
	}
	return finishManagedRelease(ctx, runtime.Store, record, run, plan, workspace, files)
}

func writeWorkspaceMetadata(workspace string, payloads map[string][]byte) error {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	for name, payload := range payloads {
		tmp, err := os.CreateTemp(workspace, "."+name+".tmp-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		cleanup := func() {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
		if _, err := tmp.Write(payload); err != nil {
			cleanup()
			return err
		}
		if err := tmp.Chmod(0o644); err != nil {
			cleanup()
			return err
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, filepath.Join(workspace, name)); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
	}
	return nil
}

func runManagedTransfer(ctx context.Context, runtime ManagedRuntime, freeze *store.RunFreeze, id string) (*store.TransferJob, error) {
	done := make(chan error, 1)
	go func() { done <- runtime.Worker.Execute(ctx, id) }()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := runtime.Store.GetTransferJob(ctx, id)
		if err != nil || job == nil {
			return nil, firstErr(err, fmt.Errorf("transfer %s disappeared", id))
		}
		freeze.BytesDone, freeze.FilesDone = job.BytesDone, job.FilesDone
		_, _ = runtime.Store.UpdateRunFreezeIfState(ctx, freeze, freeze.State)
		switch job.State {
		case store.TransferCompleted:
			return job, nil
		case store.TransferFailed, store.TransferBlocked, store.TransferCancelled:
			return job, fmt.Errorf("transfer %s ended in %s: %s", id, job.State, job.LastError)
		}
		select {
		case workerErr := <-done:
			if workerErr != nil && !strings.Contains(workerErr.Error(), "already being handled") {
				// The durable job state above remains authoritative; poll once more
				// because a concurrently running server manager may own the claim.
			}
		case <-ticker.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func finishManagedRelease(ctx context.Context, db store.Store, record *store.RunFreeze, run *store.Run, plan Plan, workspace string, files []store.RunFreezeFile) error {
	if record.State == store.RunFreezeFrozen {
		if err := transition(ctx, db, record, store.RunFreezeFrozen, store.RunFreezeAggregating); err != nil {
			return err
		}
	}
	if record.State == store.RunFreezeAggregating {
		if err := os.MkdirAll(filepath.Join(workspace, "derived"), 0o755); err != nil {
			return failFreeze(ctx, db, record, store.RunFreezeAggregating, "workspace_failed", err)
		}
		agg := osexec.CommandContext(ctx, "bash", "-lc", plan.Profile.AggregateCommand)
		agg.Dir = firstNonBlank(run.GitRepoRoot, ".")
		agg.Env = append(freezeEnv(os.Environ(), record.ID, workspace), "AEXP_RAW_MANIFEST_SHA256="+record.RawManifestSHA256)
		aggOut, aggErr := agg.CombinedOutput()
		record.AggregateResultJSON = jsonString(map[string]any{"output": tail(string(aggOut), 8192), "exit_ok": aggErr == nil})
		if aggErr != nil {
			return blockFreeze(ctx, db, record, store.RunFreezeAggregating, "aggregation_failed", fmt.Errorf("%w: %s", aggErr, tail(string(aggOut), 2048)))
		}
		derived, err := collectDerived(record.ID, record.RawManifestSHA256, workspace, plan.Profile.AggregateOutputs)
		if err != nil {
			return blockFreeze(ctx, db, record, store.RunFreezeAggregating, "derived_output_invalid", err)
		}
		files = append(files, derived...)
		if err := db.ReplaceRunFreezeFiles(ctx, record.ID, files); err != nil {
			return failFreeze(ctx, db, record, store.RunFreezeAggregating, "derived_ledger_failed", err)
		}
		if err := transition(ctx, db, record, store.RunFreezeAggregating, store.RunFreezeGateChecking); err != nil {
			return err
		}
	} else if record.State == store.RunFreezeGateChecking {
		persisted, err := db.ListRunFreezeFiles(ctx, record.ID)
		if err != nil {
			return err
		}
		files = persisted
	}
	gate := osexec.CommandContext(ctx, "bash", "-lc", plan.Profile.GateCommand)
	gate.Dir = firstNonBlank(run.GitRepoRoot, ".")
	gate.Env = append(freezeEnv(os.Environ(), record.ID, workspace), "AEXP_RAW_MANIFEST_SHA256="+record.RawManifestSHA256)
	gateOut, gateErr := gate.CombinedOutput()
	record.GateResultJSON = jsonString(map[string]any{"output": tail(string(gateOut), 8192), "exit_ok": gateErr == nil})
	if plan.Profile.GateReport != "" {
		reportPath := filepath.Join(workspace, filepath.FromSlash(plan.Profile.GateReport))
		hash, size, reportErr := hashPath(reportPath)
		if reportErr != nil {
			return blockFreeze(ctx, db, record, store.RunFreezeGateChecking, "release_gate_report_missing", reportErr)
		}
		relative, _ := filepath.Rel(workspace, reportPath)
		relative = filepath.ToSlash(relative)
		files = append(files, store.RunFreezeFile{ID: fileID(record.ID, "gate", relative), FreezeID: record.ID, Kind: "gate", Role: "release_gate", RelativePath: relative, SourceURI: reportPath, FrozenURI: reportPath, SHA256: hash, Size: size, Required: true})
		if err := db.ReplaceRunFreezeFiles(ctx, record.ID, files); err != nil {
			return failFreeze(ctx, db, record, store.RunFreezeGateChecking, "gate_ledger_failed", err)
		}
	}
	if gateErr != nil {
		return blockFreeze(ctx, db, record, store.RunFreezeGateChecking, "release_gate_failed", fmt.Errorf("%w: %s", gateErr, tail(string(gateOut), 2048)))
	}
	releaseRaw, _ := json.Marshal(map[string]any{"freeze_id": record.ID, "raw_manifest_sha256": record.RawManifestSHA256, "files": files})
	digest := sha256.Sum256(releaseRaw)
	record.ReleaseManifestSHA256 = "sha256:" + hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(workspace, "release-manifest.json"), releaseRaw, 0o644); err != nil {
		return failFreeze(ctx, db, record, store.RunFreezeGateChecking, "release_manifest_failed", err)
	}
	released := time.Now().UTC()
	record.ReleasedAt = &released
	return transition(ctx, db, record, store.RunFreezeGateChecking, store.RunFreezeReleased)
}

func resourcePathURI(resource *store.Resource, physical string) (string, error) {
	root, value := path.Clean(resource.RootDir), path.Clean(filepath.ToSlash(physical))
	if value != root && !strings.HasPrefix(value, strings.TrimRight(root, "/")+"/") {
		return "", fmt.Errorf("run source root %s escapes resource root %s", physical, resource.RootDir)
	}
	relative := strings.TrimPrefix(value, strings.TrimRight(root, "/")+"/")
	if value == root {
		relative = "."
	}
	return (&url.URL{Scheme: "resource", Host: resource.Name, Path: "/" + relative}).String(), nil
}
