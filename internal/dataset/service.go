package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

type IngestPlan struct {
	DatasetID string               `json:"dataset_id"`
	Version   string               `json:"version"`
	Source    filespace.HashResult `json:"source"`
	Transfer  transfer.Plan        `json:"transfer"`
}

type MaterializeResult struct {
	Materialization store.DatasetMaterialization `json:"materialization"`
	Transfer        *store.TransferJob           `json:"transfer,omitempty"`
	Reused          bool                         `json:"reused"`
}

type DestinationConflictError struct {
	Path     string
	Expected string
	Actual   string
}

func (e *DestinationConflictError) Error() string {
	return fmt.Sprintf("dataset destination %s contains revision %s, expected %s", e.Path, e.Actual, e.Expected)
}

type Service struct {
	Store     store.Store
	Planner   *transfer.Planner
	Transfers *transfer.Service
	Remote    filespace.RemoteFS
	Now       func() time.Time
}

func NewService(db store.Store, planner *transfer.Planner, transfers *transfer.Service, remote filespace.RemoteFS) *Service {
	return &Service{Store: db, Planner: planner, Transfers: transfers, Remote: remote, Now: time.Now}
}

func (s *Service) PlanIngest(ctx context.Context, reference, from, destination string) (IngestPlan, error) {
	datasetID, version, err := parseRef(reference)
	if err != nil {
		return IngestPlan{}, err
	}
	absolute, err := filepath.Abs(from)
	if err != nil {
		return IngestPlan{}, err
	}
	source, err := filespace.HashLocalPath(absolute, filepath.Dir(absolute))
	if err != nil {
		return IngestPlan{}, fmt.Errorf("hash dataset source: %w", err)
	}
	plan, err := s.Planner.Build(ctx, transfer.PlanRequest{
		Source: (&url.URL{Scheme: "local", Path: absolute}).String(), Destination: destination,
		SourceRevision: source.Revision, Initiator: "mac", Verification: "manifest",
	})
	if err != nil {
		return IngestPlan{}, err
	}
	return IngestPlan{DatasetID: datasetID, Version: version, Source: source, Transfer: plan}, nil
}

func (s *Service) StartIngest(ctx context.Context, reference, from, destination, expectedPlanSHA256 string) (*store.TransferJob, bool, error) {
	planned, err := s.PlanIngest(ctx, reference, from, destination)
	if err != nil {
		return nil, false, err
	}
	if planned.Transfer.PlanSHA256 != expectedPlanSHA256 {
		return nil, false, &transfer.PlanHashMismatchError{Expected: expectedPlanSHA256, Actual: planned.Transfer.PlanSHA256}
	}
	return s.Transfers.Create(ctx, transfer.PlanRequest{
		Source: planned.Transfer.Source.URI, Destination: planned.Transfer.Destination.URI,
		SourceRevision: planned.Source.Revision, Initiator: "mac", Verification: "manifest",
	}, expectedPlanSHA256)
}

func (s *Service) FinalizeIngest(ctx context.Context, reference, transferID, format string) (*store.DatasetVersion, bool, error) {
	datasetID, version, err := parseRef(reference)
	if err != nil {
		return nil, false, err
	}
	detail, err := s.Transfers.Get(ctx, transferID)
	if err != nil || detail == nil {
		if err == nil {
			err = fmt.Errorf("transfer %s not found", transferID)
		}
		return nil, false, err
	}
	if detail.Job.State != store.TransferCompleted {
		return nil, false, fmt.Errorf("transfer %s is %s, not completed", transferID, detail.Job.State)
	}
	plan, err := transfer.DecodePlan(detail.Plan)
	if err != nil {
		return nil, false, err
	}
	if plan.Destination.Scheme != filespace.Scheme || plan.Destination.StorageTargetID == "" {
		return nil, false, fmt.Errorf("dataset ingest destination must be a logical path on managed storage")
	}
	target, err := s.Store.GetStorageTarget(ctx, plan.Destination.StorageTargetID)
	if err != nil || target == nil {
		if err == nil {
			err = fmt.Errorf("storage target %s not found", plan.Destination.StorageTargetID)
		}
		return nil, false, err
	}
	storagePath := strings.TrimPrefix(plan.Destination.PhysicalPath, strings.TrimRight(target.RootPath, "/")+"/")
	if storagePath == plan.Destination.PhysicalPath || storagePath == "" {
		return nil, false, fmt.Errorf("dataset destination is outside the storage target root")
	}
	metadata, _ := json.Marshal(map[string]any{"logical_uri": plan.Destination.URI, "revision": plan.Source.Revision, "transfer_id": transferID})
	digest := sha256.Sum256([]byte(datasetID + "\x00" + version + "\x00" + plan.Source.Revision))
	dataset := &store.DatasetVersion{
		ID: fmt.Sprintf("dataset_%x", digest[:8]), DatasetID: datasetID, Version: version,
		StorageTargetID: target.ID, StoragePath: storagePath, LogicalURI: plan.Destination.URI,
		Revision: plan.Source.Revision, ManifestSHA256: plan.Source.Revision, Format: format,
		FileCount: detail.Job.FileCount, TotalBytes: detail.Job.TotalBytes,
		State: store.DatasetStateVerified, ManifestJSON: string(metadata),
	}
	return s.Store.CreateDatasetVersionImmutable(ctx, dataset)
}

func (s *Service) Materialize(ctx context.Context, reference, resourceID, destination string) (MaterializeResult, error) {
	datasetID, version, err := parseRef(reference)
	if err != nil {
		return MaterializeResult{}, err
	}
	dataset, err := s.Store.GetDatasetVersionByRef(ctx, datasetID, version)
	if err != nil || dataset == nil {
		if err == nil {
			err = fmt.Errorf("dataset %s not found", reference)
		}
		return MaterializeResult{}, err
	}
	revision := dataset.Revision
	if revision == "" {
		revision = dataset.ManifestSHA256
	}
	if revision == "" {
		return MaterializeResult{}, fmt.Errorf("dataset %s has no pinned revision", reference)
	}
	resource, err := s.Store.GetResource(ctx, resourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", resourceID)
		}
		return MaterializeResult{}, err
	}
	physical, relative, err := materializationPath(resource, destination, datasetID, version)
	if err != nil {
		return MaterializeResult{}, err
	}
	logicalURI := dataset.LogicalURI
	sourceURI := logicalURI
	if sourceURI == "" {
		target, err := s.Store.GetStorageTarget(ctx, dataset.StorageTargetID)
		if err != nil || target == nil {
			return MaterializeResult{}, fmt.Errorf("legacy dataset storage target is unavailable")
		}
		sourceURI = (&url.URL{Scheme: "storage", Host: target.Name, Path: "/" + dataset.StoragePath}).String()
	}
	now := s.now()
	materialization := store.DatasetMaterialization{
		ID: materializationID(dataset.ID, resource.ID), DatasetVersionID: dataset.ID, ResourceID: resource.ID,
		LocalPath: physical, State: store.MaterializationPlanned, UpdatedAt: now,
	}
	if s.Remote == nil {
		return MaterializeResult{}, fmt.Errorf("remote filesystem is unavailable")
	}
	location := filespace.RemoteLocation{Resource: resource, PhysicalPath: physical, Boundary: resource.RootDir}
	entry, err := s.Remote.Stat(ctx, location)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("inspect dataset destination: %w", err)
	}
	if entry.Exists {
		hashed, err := s.Remote.Hash(ctx, location)
		if err != nil {
			return MaterializeResult{}, fmt.Errorf("hash dataset destination: %w", err)
		}
		if hashed.Revision != revision {
			return MaterializeResult{}, &DestinationConflictError{Path: physical, Expected: revision, Actual: hashed.Revision}
		}
		materialization.State, materialization.BytesPresent, materialization.VerifiedSHA256 = store.MaterializationReady, hashed.TotalBytes, hashed.Revision
		materialization.StartedAt, materialization.FinishedAt, materialization.VerifiedAt, materialization.LastAccessedAt = &now, &now, &now, &now
		if err := s.Store.SaveDatasetMaterialization(ctx, &materialization); err != nil {
			return MaterializeResult{}, err
		}
		if err := s.recordCachePlacement(ctx, dataset, resource, physical, hashed, now); err != nil {
			return MaterializeResult{}, err
		}
		return MaterializeResult{Materialization: materialization, Reused: true}, nil
	}
	destinationURI := (&url.URL{Scheme: "resource", Host: resource.Name, Path: "/" + relative}).String()
	planRequest := transfer.PlanRequest{Source: sourceURI, Destination: destinationURI, SourceRevision: revision, Initiator: "auto", Verification: "manifest"}
	planned, err := s.Planner.Build(ctx, planRequest)
	if err != nil {
		return MaterializeResult{}, err
	}
	job, _, err := s.Transfers.Create(ctx, planRequest, planned.PlanSHA256)
	if err != nil {
		return MaterializeResult{}, err
	}
	materialization.State, materialization.TransferID, materialization.StartedAt = store.MaterializationTransferring, job.ID, &now
	if job.State == store.TransferCompleted {
		materialization.State, materialization.BytesPresent, materialization.VerifiedSHA256 = store.MaterializationReady, job.TotalBytes, revision
		materialization.FinishedAt, materialization.VerifiedAt, materialization.LastAccessedAt = &now, &now, &now
	}
	if err := s.Store.SaveDatasetMaterialization(ctx, &materialization); err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{Materialization: materialization, Transfer: job}, nil
}

func (s *Service) ReconcileMaterialization(ctx context.Context, datasetVersionID, resourceID string) (*store.DatasetMaterialization, error) {
	materialization, err := s.Store.GetDatasetMaterialization(ctx, datasetVersionID, resourceID)
	if err != nil || materialization == nil || materialization.TransferID == "" {
		return materialization, err
	}
	job, err := s.Store.GetTransferJob(ctx, materialization.TransferID)
	if err != nil || job == nil {
		return materialization, err
	}
	dataset, err := s.Store.GetDatasetVersion(ctx, datasetVersionID)
	if err != nil || dataset == nil {
		return nil, err
	}
	now := s.now()
	switch job.State {
	case store.TransferCompleted:
		materialization.State, materialization.BytesPresent = store.MaterializationReady, job.TotalBytes
		materialization.VerifiedSHA256 = dataset.Revision
		if materialization.VerifiedSHA256 == "" {
			materialization.VerifiedSHA256 = dataset.ManifestSHA256
		}
		materialization.FinishedAt, materialization.VerifiedAt, materialization.LastAccessedAt = &now, &now, &now
	case store.TransferFailed, store.TransferBlocked, store.TransferCancelled:
		materialization.State, materialization.LastError, materialization.FinishedAt = store.MaterializationFailed, job.LastError, &now
	case store.TransferVerifying, store.TransferPromoting:
		materialization.State = store.MaterializationVerifying
	default:
		materialization.State = store.MaterializationTransferring
	}
	if err := s.Store.SaveDatasetMaterialization(ctx, materialization); err != nil {
		return nil, err
	}
	if materialization.State == store.MaterializationReady && dataset.LogicalURI != "" {
		resource, _ := s.Store.GetResource(ctx, resourceID)
		if resource != nil {
			hashed := filespace.HashResult{Revision: materialization.VerifiedSHA256, ManifestSHA256: materialization.VerifiedSHA256, TotalBytes: materialization.BytesPresent}
			if err := s.recordCachePlacement(ctx, dataset, resource, materialization.LocalPath, hashed, now); err != nil {
				return nil, err
			}
		}
	}
	return materialization, nil
}

// Verify checks an existing compute cache against the immutable dataset
// revision. It never creates a transfer; repair/materialize is a separate
// explicit operation.
func (s *Service) Verify(ctx context.Context, reference, resourceID, destination string) (*store.DatasetMaterialization, error) {
	datasetID, version, err := parseRef(reference)
	if err != nil {
		return nil, err
	}
	dataset, err := s.Store.GetDatasetVersionByRef(ctx, datasetID, version)
	if err != nil || dataset == nil {
		return nil, firstDatasetErr(err, fmt.Errorf("dataset %s not found", reference))
	}
	resource, err := s.Store.GetResource(ctx, resourceID)
	if err != nil || resource == nil {
		return nil, firstDatasetErr(err, fmt.Errorf("resource %s not found", resourceID))
	}
	physical, _, err := materializationPath(resource, destination, datasetID, version)
	if err != nil {
		return nil, err
	}
	if s.Remote == nil {
		return nil, fmt.Errorf("remote filesystem is unavailable")
	}
	now := s.now()
	materialization := &store.DatasetMaterialization{ID: materializationID(dataset.ID, resource.ID), DatasetVersionID: dataset.ID, ResourceID: resource.ID, LocalPath: physical, State: store.MaterializationVerifying, UpdatedAt: now}
	location := filespace.RemoteLocation{Resource: resource, PhysicalPath: physical, Boundary: resource.RootDir}
	entry, err := s.Remote.Stat(ctx, location)
	if err != nil {
		materialization.State, materialization.LastError = store.MaterializationFailed, err.Error()
		_ = s.Store.SaveDatasetMaterialization(context.Background(), materialization)
		return materialization, err
	}
	if !entry.Exists {
		materialization.State, materialization.LastError = store.MaterializationFailed, "dataset cache is missing"
		_ = s.Store.SaveDatasetMaterialization(ctx, materialization)
		return materialization, fmt.Errorf("dataset cache %s is missing", physical)
	}
	hashed, err := s.Remote.Hash(ctx, location)
	if err != nil {
		materialization.State, materialization.LastError = store.MaterializationFailed, err.Error()
		_ = s.Store.SaveDatasetMaterialization(context.Background(), materialization)
		return materialization, err
	}
	expected := dataset.Revision
	if expected == "" {
		expected = dataset.ManifestSHA256
	}
	if hashed.Revision != expected {
		conflict := &DestinationConflictError{Path: physical, Expected: expected, Actual: hashed.Revision}
		materialization.State, materialization.LastError, materialization.BytesPresent, materialization.VerifiedSHA256 = store.MaterializationFailed, conflict.Error(), hashed.TotalBytes, hashed.Revision
		_ = s.Store.SaveDatasetMaterialization(ctx, materialization)
		return materialization, conflict
	}
	materialization.State, materialization.LastError = store.MaterializationReady, ""
	materialization.BytesPresent, materialization.VerifiedSHA256 = hashed.TotalBytes, hashed.Revision
	materialization.VerifiedAt, materialization.LastAccessedAt = &now, &now
	if err := s.Store.SaveDatasetMaterialization(ctx, materialization); err != nil {
		return nil, err
	}
	if err := s.recordCachePlacement(ctx, dataset, resource, physical, hashed, now); err != nil {
		return nil, err
	}
	return materialization, nil
}

func firstDatasetErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func (s *Service) recordCachePlacement(ctx context.Context, dataset *store.DatasetVersion, resource *store.Resource, physical string, hashed filespace.HashResult, now time.Time) error {
	if dataset.LogicalURI == "" {
		return nil
	}
	digest := sha256.Sum256([]byte(dataset.LogicalURI + "\x00" + resource.ID + "\x00" + physical))
	placement := &store.PathPlacement{
		ID: fmt.Sprintf("placement_%x", digest[:8]), LogicalURI: dataset.LogicalURI, ResourceID: resource.ID,
		PhysicalPath: physical, Role: store.PlacementRoleCache, DesiredState: store.PlacementDesiredPresent,
	}
	if err := s.Store.SavePathPlacement(ctx, placement); err != nil {
		return err
	}
	_, err := s.Store.UpdatePathPlacementObservation(ctx, placement.ID, store.PlacementObservation{
		State: store.PlacementObservedPresent, Revision: hashed.Revision, ManifestSHA256: hashed.ManifestSHA256,
		BytesPresent: hashed.TotalBytes, Source: "dataset_materialize", ObservedAt: &now, CheckedAt: now,
	})
	return err
}

func materializationPath(resource *store.Resource, requested, datasetID, version string) (string, string, error) {
	relative := requested
	if relative == "" {
		relative = path.Join("datasets", datasetID, version)
	}
	if path.IsAbs(relative) {
		cleanRoot, cleanRequested := path.Clean(resource.RootDir), path.Clean(relative)
		if cleanRequested != cleanRoot && !strings.HasPrefix(cleanRequested, strings.TrimRight(cleanRoot, "/")+"/") {
			return "", "", fmt.Errorf("materialization destination escapes resource root")
		}
		relative = strings.TrimPrefix(cleanRequested, strings.TrimRight(cleanRoot, "/")+"/")
	}
	if relative == "" || relative == "." || path.IsAbs(relative) || strings.ContainsAny(relative, "\\\x00\r\n") {
		return "", "", fmt.Errorf("materialization destination is unsafe")
	}
	for _, segment := range strings.Split(relative, "/") {
		if segment == ".." {
			return "", "", fmt.Errorf("materialization destination is unsafe")
		}
	}
	relative = path.Clean(relative)
	return path.Join(resource.RootDir, relative), relative, nil
}

func materializationID(datasetVersionID, resourceID string) string {
	digest := sha256.Sum256([]byte(datasetVersionID + "\x00" + resourceID))
	return fmt.Sprintf("materialization_%x", digest[:8])
}

func parseRef(reference string) (string, string, error) {
	parts := strings.Split(reference, "@")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("dataset reference must be name@version")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
