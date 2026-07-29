package runio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

// Service is the Run-specific compatibility adapter over the shared
// LogicalPath/Placement/TransferJob implementation.
type Service struct {
	Store     store.Store
	Files     *filespace.Service
	Planner   *transfer.Planner
	Transfers *transfer.Service
	Worker    *transfer.Worker
	Remote    filespace.RemoteFS
	PollEvery time.Duration

	finalizationMu    sync.Mutex
	finalizationLocks map[string]*runFinalizationLock
}

type runFinalizationLock struct {
	mu   sync.Mutex
	refs int
}

func NewService(db store.Store, files *filespace.Service, planner *transfer.Planner, transfers *transfer.Service, worker *transfer.Worker, remote filespace.RemoteFS) *Service {
	return &Service{Store: db, Files: files, Planner: planner, Transfers: transfers, Worker: worker, Remote: remote, PollEvery: 200 * time.Millisecond, finalizationLocks: make(map[string]*runFinalizationLock)}
}

func (s *Service) EnsureInputs(ctx context.Context, run *store.Run, resource *store.Resource) error {
	bindings, err := s.Store.ListRunInputBindings(ctx, run.ID)
	if err != nil {
		return err
	}
	blockers := make([]executor.RunPreflightBlocker, 0)
	for index := range bindings {
		binding := &bindings[index]
		if err := s.ensureInput(ctx, run, resource, binding); err != nil {
			binding.State, binding.ErrorCode, binding.LastError = store.RunBindingBlocked, "input_unavailable", err.Error()
			_ = s.Store.UpdateRunInputBinding(context.Background(), binding)
			blockers = append(blockers, executor.RunPreflightBlocker{Code: "input_unavailable", Message: fmt.Sprintf("input %d (%s): %v", binding.Ordinal, binding.LogicalURI, err)})
			break
		}
	}
	if len(blockers) > 0 {
		return &executor.RunPreflightBlockedError{Blockers: blockers}
	}
	return nil
}

func (s *Service) ensureInput(ctx context.Context, run *store.Run, resource *store.Resource, binding *store.RunInputBinding) error {
	if s.Remote == nil || s.Planner == nil || s.Transfers == nil {
		return fmt.Errorf("managed transfer service is unavailable")
	}
	if _, err := filespace.Parse(binding.LogicalURI); err != nil {
		return err
	}
	if binding.Revision == "" {
		return fmt.Errorf("pinned revision is required")
	}
	relative, err := safeRelative(binding.TargetPath)
	if err != nil {
		return err
	}
	physical := path.Join(resource.RootDir, relative)
	location := filespace.RemoteLocation{Resource: resource, PhysicalPath: physical, Boundary: resource.RootDir}
	entry, err := s.Remote.Stat(ctx, location)
	if err != nil {
		return fmt.Errorf("inspect destination: %w", err)
	}
	destinationPlacement := cachePlacement(binding.LogicalURI, resource.ID, physical)
	binding.DestinationPlacementID = destinationPlacement.ID
	if entry.Exists {
		hashed, err := s.Remote.Hash(ctx, location)
		if err != nil {
			return fmt.Errorf("hash destination: %w", err)
		}
		if hashed.Revision != binding.Revision {
			return fmt.Errorf("destination revision conflict: found %s, expected %s", hashed.Revision, binding.Revision)
		}
		now := time.Now().UTC()
		destinationPlacement.ObservedState, destinationPlacement.Revision = store.PlacementObservedPresent, hashed.Revision
		destinationPlacement.ManifestSHA256, destinationPlacement.BytesPresent = hashed.ManifestSHA256, hashed.TotalBytes
		destinationPlacement.ObservationSource, destinationPlacement.ObservedAt, destinationPlacement.CheckedAt = "run_input_cache_verify", &now, &now
		if err := s.Store.SavePathPlacement(ctx, &destinationPlacement); err != nil {
			return err
		}
		binding.State, binding.VerifiedAt = store.RunBindingReady, &now
		binding.ErrorCode, binding.LastError = "", ""
		return s.Store.UpdateRunInputBinding(ctx, binding)
	}
	destinationURI := resourceURI(resource, relative)
	request := transfer.PlanRequest{Source: binding.LogicalURI, Destination: destinationURI, SourceRevision: binding.Revision, Initiator: "auto", Verification: "manifest"}
	plan, err := s.Planner.Build(ctx, request)
	if err != nil {
		return err
	}
	if len(plan.Blockers) > 0 {
		return fmt.Errorf("transfer plan blocked: %s", blockerText(plan.Blockers))
	}
	binding.SourcePlacementID, binding.DestinationPlacementID = plan.Source.PlacementID, destinationPlacement.ID
	job, _, err := s.Transfers.Create(ctx, request, plan.PlanSHA256)
	if err != nil {
		return err
	}
	binding.TransferID, binding.State = job.ID, store.RunBindingEnsuring
	if err := s.Store.UpdateRunInputBinding(ctx, binding); err != nil {
		return err
	}
	job, err = s.executeAndWait(ctx, job)
	if err != nil {
		return err
	}
	if job.State != store.TransferCompleted {
		return fmt.Errorf("transfer %s ended in %s: %s", job.ID, job.State, job.LastError)
	}
	now := time.Now().UTC()
	destinationPlacement.ObservedState, destinationPlacement.Revision = store.PlacementObservedPresent, binding.Revision
	destinationPlacement.ManifestSHA256, destinationPlacement.BytesPresent = binding.Revision, job.TotalBytes
	destinationPlacement.ObservationSource, destinationPlacement.ObservedAt, destinationPlacement.CheckedAt = "run_input_transfer", &now, &now
	if err := s.Store.SavePathPlacement(ctx, &destinationPlacement); err != nil {
		return err
	}
	binding.State, binding.VerifiedAt = store.RunBindingReady, &now
	binding.ErrorCode, binding.LastError = "", ""
	return s.Store.UpdateRunInputBinding(ctx, binding)
}

func (s *Service) FinalizeOutputs(ctx context.Context, run *store.Run, resource *store.Resource) error {
	if run == nil || strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("run is required for output finalization")
	}
	release := s.acquireFinalization(run.ID)
	defer release()

	current, err := s.Store.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("run %s not found", run.ID)
	}
	run = current
	switch run.DataFinalizationState {
	case store.RunDataFinalizationCompleted, store.RunDataFinalizationSkipped:
		return nil
	case store.RunDataFinalizationBlocked, store.RunDataFinalizationFailed:
		if strings.TrimSpace(run.DataFinalizationError) != "" {
			return fmt.Errorf("%s", run.DataFinalizationError)
		}
		return fmt.Errorf("run %s data finalization is %s", run.ID, run.DataFinalizationState)
	}

	bindings, err := s.Store.ListRunOutputBindings(ctx, run.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if len(bindings) == 0 {
		run.DataFinalizationState, run.DataFinalizationError, run.DataFinalizationUpdatedAt = store.RunDataFinalizationSkipped, "", &now
		return s.Store.UpdateRunDataFinalization(ctx, run.ID, run.DataFinalizationState, run.DataFinalizationError, now)
	}
	run.DataFinalizationState, run.DataFinalizationError, run.DataFinalizationUpdatedAt = store.RunDataFinalizationPublishing, "", &now
	if err := s.Store.UpdateRunDataFinalization(ctx, run.ID, run.DataFinalizationState, run.DataFinalizationError, now); err != nil {
		return err
	}
	artifacts, err := s.Store.ListArtifacts(ctx, run.ID)
	if err != nil {
		return s.finishFinalization(ctx, run, store.RunDataFinalizationFailed, err)
	}
	for index := range bindings {
		binding := &bindings[index]
		matched := matchingArtifacts(artifacts, binding.SourcePattern)
		if len(matched) == 0 {
			binding.State, binding.ErrorCode, binding.LastError = store.RunBindingMissing, "output_missing", "no discovered artifact matches the declared output"
			_ = s.Store.UpdateRunOutputBinding(ctx, binding)
			if binding.Required {
				return s.finishFinalization(ctx, run, store.RunDataFinalizationBlocked, fmt.Errorf("required output %s is missing", binding.SourcePattern))
			}
			continue
		}
		if err := s.publishOutput(ctx, run, resource, binding, matched); err != nil {
			binding.State, binding.ErrorCode, binding.LastError = store.RunBindingFailed, "output_publish_failed", err.Error()
			_ = s.Store.UpdateRunOutputBinding(context.Background(), binding)
			if binding.Required {
				return s.finishFinalization(ctx, run, store.RunDataFinalizationFailed, err)
			}
		}
	}
	return s.finishFinalization(ctx, run, store.RunDataFinalizationCompleted, nil)
}

func (s *Service) acquireFinalization(runID string) func() {
	s.finalizationMu.Lock()
	if s.finalizationLocks == nil {
		s.finalizationLocks = make(map[string]*runFinalizationLock)
	}
	entry := s.finalizationLocks[runID]
	if entry == nil {
		entry = &runFinalizationLock{}
		s.finalizationLocks[runID] = entry
	}
	entry.refs++
	s.finalizationMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.finalizationMu.Lock()
		entry.refs--
		if entry.refs == 0 && s.finalizationLocks[runID] == entry {
			delete(s.finalizationLocks, runID)
		}
		s.finalizationMu.Unlock()
	}
}

func (s *Service) publishOutput(ctx context.Context, run *store.Run, resource *store.Resource, binding *store.RunOutputBinding, artifacts []store.Artifact) error {
	root := strings.TrimSpace(run.ResolvedCwd)
	if root == "" {
		root = strings.TrimSpace(run.Cwd)
	}
	if root == "" {
		return fmt.Errorf("run output root is unavailable")
	}
	selection := make([]transfer.ManifestEntry, 0, len(artifacts))
	for _, artifact := range artifacts {
		selection = append(selection, transfer.ManifestEntry{Path: artifact.RelativePath, Type: "file", SHA256: artifact.SHA256, Size: artifact.Size})
	}
	normalized, revision, _, _, err := transfer.NormalizeSelection(selection)
	if err != nil {
		return err
	}
	sourceURI := resourceURI(resource, strings.TrimPrefix(root, strings.TrimRight(resource.RootDir, "/")+"/"))
	request := transfer.PlanRequest{Source: sourceURI, Destination: binding.LogicalURI, SourceRevision: revision, Initiator: "auto", Verification: "manifest", Selection: normalized}
	// A single exact file is transported as a file rather than a one-entry
	// directory projection, preserving file-to-file URI semantics.
	if len(artifacts) == 1 && !strings.ContainsAny(binding.SourcePattern, "*?[") {
		relative := strings.TrimPrefix(artifacts[0].Path, strings.TrimRight(resource.RootDir, "/")+"/")
		sourceURI = resourceURI(resource, relative)
		request.Source, request.SourceRevision, request.Selection = sourceURI, artifacts[0].SHA256, nil
		revision = artifacts[0].SHA256
	}
	plan, err := s.Planner.Build(ctx, request)
	if err != nil {
		return err
	}
	if len(plan.Blockers) > 0 {
		return fmt.Errorf("output transfer plan blocked: %s", blockerText(plan.Blockers))
	}
	job, _, err := s.Transfers.Create(ctx, request, plan.PlanSHA256)
	if err != nil {
		return err
	}
	binding.State, binding.Revision, binding.TransferID = store.RunBindingPublishing, revision, job.ID
	binding.SourcePlacementID, binding.DestinationPlacementID = plan.Source.PlacementID, plan.Destination.PlacementID
	if err := s.Store.UpdateRunOutputBinding(ctx, binding); err != nil {
		return err
	}
	job, err = s.executeAndWait(ctx, job)
	if err != nil {
		return err
	}
	if job.State != store.TransferCompleted {
		return fmt.Errorf("transfer %s ended in %s: %s", job.ID, job.State, job.LastError)
	}
	now := time.Now().UTC()
	binding.State, binding.PublishedAt = store.RunBindingPublished, &now
	binding.ErrorCode, binding.LastError = "", ""
	return s.Store.UpdateRunOutputBinding(ctx, binding)
}

func (s *Service) finishFinalization(ctx context.Context, run *store.Run, state string, finalErr error) error {
	now := time.Now().UTC()
	run.DataFinalizationState, run.DataFinalizationUpdatedAt = state, &now
	run.DataFinalizationError = ""
	if finalErr != nil {
		run.DataFinalizationError = finalErr.Error()
	}
	if err := s.Store.UpdateRunDataFinalization(ctx, run.ID, run.DataFinalizationState, run.DataFinalizationError, now); err != nil {
		return err
	}
	return finalErr
}

func (s *Service) executeAndWait(ctx context.Context, job *store.TransferJob) (*store.TransferJob, error) {
	if job.State == store.TransferCompleted {
		return job, nil
	}
	if s.Worker != nil && job.State == store.TransferQueued {
		_ = s.Worker.Execute(ctx, job.ID)
	}
	interval := s.PollEvery
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		current, err := s.Store.GetTransferJob(ctx, job.ID)
		if err != nil || current == nil {
			if err == nil {
				err = fmt.Errorf("transfer %s disappeared", job.ID)
			}
			return nil, err
		}
		if current.State == store.TransferCompleted || current.State == store.TransferBlocked || current.State == store.TransferFailed || current.State == store.TransferCancelled {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func safeRelative(value string) (string, error) {
	value = path.Clean(strings.TrimSpace(value))
	if value == "" || value == "." || path.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../") || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("target path must be a safe resource-relative path")
	}
	return value, nil
}

func resourceURI(resource *store.Resource, relative string) string {
	return (&url.URL{Scheme: "resource", Host: resource.Name, Path: "/" + strings.TrimPrefix(path.Clean(relative), "/")}).String()
}

func cachePlacement(logicalURI, resourceID, physical string) store.PathPlacement {
	digest := sha256.Sum256([]byte(logicalURI + "\x00" + resourceID + "\x00" + physical))
	return store.PathPlacement{ID: "placement_" + hex.EncodeToString(digest[:8]), LogicalURI: logicalURI, ResourceID: resourceID, PhysicalPath: physical, Role: store.PlacementRoleCache, DesiredState: store.PlacementDesiredPresent, ObservedState: store.PlacementObservedUnknown}
}

func blockerText(blockers []transfer.Blocker) string {
	parts := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		parts = append(parts, blocker.Code+": "+blocker.Message)
	}
	return strings.Join(parts, "; ")
}

func matchingArtifacts(artifacts []store.Artifact, pattern string) []store.Artifact {
	expression := regexp.QuoteMeta(path.Clean(pattern))
	expression = strings.ReplaceAll(expression, `\*\*`, `.*`)
	expression = strings.ReplaceAll(expression, `\*`, `[^/]*`)
	expression = strings.ReplaceAll(expression, `\?`, `[^/]`)
	matcher, err := regexp.Compile("^" + expression + "$")
	if err != nil {
		return nil
	}
	matched := make([]store.Artifact, 0)
	for _, artifact := range artifacts {
		if matcher.MatchString(path.Clean(artifact.RelativePath)) {
			matched = append(matched, artifact)
		}
	}
	return matched
}
