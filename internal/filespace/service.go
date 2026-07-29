package filespace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type ResolvedPath struct {
	LogicalURI       string                `json:"logical_uri"`
	Root             store.LogicalRoot     `json:"root"`
	DefaultPlacement store.PathPlacement   `json:"default_placement"`
	Placements       []store.PathPlacement `json:"placements"`
}

type PlacementView struct {
	store.PathPlacement
	Freshness string `json:"freshness"`
}

type InspectResult struct {
	Placement PlacementView `json:"placement"`
}

type Service struct {
	Store          store.Store
	Remote         RemoteFS
	ObservationTTL time.Duration
	Now            func() time.Time
}

func NewService(db store.Store, remote RemoteFS) *Service {
	return &Service{Store: db, Remote: remote, ObservationTTL: 5 * time.Minute, Now: time.Now}
}

func (s *Service) Resolve(ctx context.Context, rawURI string) (ResolvedPath, error) {
	logical, err := Parse(rawURI)
	if err != nil {
		return ResolvedPath{}, err
	}
	roots, err := s.Store.ListLogicalRoots(ctx, logical.Workspace)
	if err != nil {
		return ResolvedPath{}, err
	}
	root, suffix, err := ResolveRoot(logical, roots)
	if err != nil {
		return ResolvedPath{}, err
	}
	target, err := s.Store.GetStorageTarget(ctx, root.StorageTargetID)
	if err != nil || target == nil {
		if err == nil {
			err = fmt.Errorf("storage target %s not found", root.StorageTargetID)
		}
		return ResolvedPath{}, err
	}
	resource, err := s.Store.GetResource(ctx, target.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("storage resource %s not found", target.ResourceID)
		}
		return ResolvedPath{}, err
	}
	relativePhysical, err := PhysicalPath(root, suffix)
	if err != nil {
		return ResolvedPath{}, err
	}
	canonical := logical.String()
	placements, err := s.Store.ListPathPlacements(ctx, canonical)
	if err != nil {
		return ResolvedPath{}, err
	}
	defaultPlacement := store.PathPlacement{
		ID:              placementID(canonical, resource.ID, path.Join(target.RootPath, relativePhysical)),
		LogicalURI:      canonical,
		ResourceID:      resource.ID,
		StorageTargetID: target.ID,
		PhysicalPath:    path.Join(target.RootPath, relativePhysical),
		Role:            store.PlacementRoleAuthoritative,
		DesiredState:    store.PlacementDesiredPresent,
		ObservedState:   store.PlacementObservedUnknown,
	}
	return ResolvedPath{LogicalURI: canonical, Root: root, DefaultPlacement: defaultPlacement, Placements: placements}, nil
}

func (s *Service) Locate(ctx context.Context, rawURI string) ([]PlacementView, error) {
	resolved, err := s.Resolve(ctx, rawURI)
	if err != nil {
		return nil, err
	}
	placements := append([]store.PathPlacement(nil), resolved.Placements...)
	foundDefault := false
	for _, placement := range placements {
		if placement.ID == resolved.DefaultPlacement.ID {
			foundDefault = true
			break
		}
	}
	if !foundDefault {
		placements = append(placements, resolved.DefaultPlacement)
	}
	views := make([]PlacementView, 0, len(placements))
	for _, placement := range placements {
		views = append(views, PlacementView{PathPlacement: placement, Freshness: s.freshness(placement.CheckedAt)})
	}
	return views, nil
}

func (s *Service) Inspect(ctx context.Context, rawURI, resourceID string) (InspectResult, error) {
	resolved, err := s.Resolve(ctx, rawURI)
	if err != nil {
		return InspectResult{}, err
	}
	placement, err := selectPlacement(resolved, resourceID)
	if err != nil {
		return InspectResult{}, err
	}
	if err := s.Store.SavePathPlacement(ctx, &placement); err != nil {
		return InspectResult{}, err
	}
	resource, err := s.Store.GetResource(ctx, placement.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", placement.ResourceID)
		}
		return InspectResult{}, err
	}
	now := s.now()
	location, err := s.remoteLocation(ctx, resource, placement)
	if err != nil {
		return InspectResult{}, err
	}
	entry, statErr := s.Remote.Stat(ctx, location)
	observation := store.PlacementObservation{State: store.PlacementObservedUnknown, Source: "remote_stat", CheckedAt: now, ObservedAt: &now}
	if statErr != nil {
		observation.Error = statErr.Error()
		var remoteErr *RemoteError
		if errors.As(statErr, &remoteErr) && remoteErr.Kind == RemoteErrorUnreachable {
			observation.State = store.PlacementObservedUnreachable
		}
	} else if !entry.Exists {
		observation.State = store.PlacementObservedMissing
	} else {
		observation.State = store.PlacementObservedPresent
		observation.BytesPresent = entry.Size
	}
	updated, err := s.Store.UpdatePathPlacementObservation(ctx, placement.ID, observation)
	if err != nil {
		return InspectResult{}, err
	}
	if !updated {
		return InspectResult{}, fmt.Errorf("placement observation was superseded by a newer probe")
	}
	placement, err = dereferencePlacement(s.Store.GetPathPlacement(ctx, placement.ID))
	if err != nil {
		return InspectResult{}, err
	}
	return InspectResult{Placement: PlacementView{PathPlacement: placement, Freshness: s.freshness(placement.CheckedAt)}}, nil
}

func (s *Service) Hash(ctx context.Context, rawURI, resourceID string) (HashResult, error) {
	resolved, err := s.Resolve(ctx, rawURI)
	if err != nil {
		return HashResult{}, err
	}
	placement, err := selectPlacement(resolved, resourceID)
	if err != nil {
		return HashResult{}, err
	}
	if err := s.Store.SavePathPlacement(ctx, &placement); err != nil {
		return HashResult{}, err
	}
	resource, err := s.Store.GetResource(ctx, placement.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", placement.ResourceID)
		}
		return HashResult{}, err
	}
	location, err := s.remoteLocation(ctx, resource, placement)
	if err != nil {
		return HashResult{}, err
	}
	result, hashErr := s.Remote.Hash(ctx, location)
	now := s.now()
	observation := store.PlacementObservation{State: store.PlacementObservedUnknown, Source: "remote_hash", CheckedAt: now, ObservedAt: &now}
	if hashErr != nil {
		observation.Error = hashErr.Error()
		var remoteErr *RemoteError
		if errors.As(hashErr, &remoteErr) && remoteErr.Kind == RemoteErrorUnreachable {
			observation.State = store.PlacementObservedUnreachable
		}
	} else {
		observation.State = store.PlacementObservedPresent
		observation.Revision = result.Revision
		observation.ManifestSHA256 = result.ManifestSHA256
		observation.BytesPresent = result.TotalBytes
	}
	if _, err := s.Store.UpdatePathPlacementObservation(ctx, placement.ID, observation); err != nil {
		return HashResult{}, err
	}
	if hashErr != nil {
		return HashResult{}, hashErr
	}
	return result, nil
}

func (s *Service) List(ctx context.Context, rawURI, resourceID, cursor string, limit int) (ListResult, error) {
	resolved, err := s.Resolve(ctx, rawURI)
	if err != nil {
		return ListResult{}, err
	}
	placement, err := selectPlacement(resolved, resourceID)
	if err != nil {
		return ListResult{}, err
	}
	resource, err := s.Store.GetResource(ctx, placement.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", placement.ResourceID)
		}
		return ListResult{}, err
	}
	location, err := s.remoteLocation(ctx, resource, placement)
	if err != nil {
		return ListResult{}, err
	}
	return s.Remote.List(ctx, location, cursor, limit)
}

func (s *Service) remoteLocation(ctx context.Context, resource *store.Resource, placement store.PathPlacement) (RemoteLocation, error) {
	boundary := resource.RootDir
	if placement.StorageTargetID != "" {
		target, err := s.Store.GetStorageTarget(ctx, placement.StorageTargetID)
		if err != nil {
			return RemoteLocation{}, err
		}
		if target == nil || target.ResourceID != resource.ID {
			return RemoteLocation{}, fmt.Errorf("placement storage boundary is unavailable")
		}
		boundary = target.RootPath
	}
	cleanBoundary := path.Clean(boundary)
	cleanPhysical := path.Clean(placement.PhysicalPath)
	if cleanBoundary == "." || cleanBoundary == "/" || (cleanPhysical != cleanBoundary && !strings.HasPrefix(cleanPhysical, strings.TrimRight(cleanBoundary, "/")+"/")) {
		return RemoteLocation{}, fmt.Errorf("placement path %s escapes managed boundary %s", placement.PhysicalPath, boundary)
	}
	return RemoteLocation{Resource: resource, PhysicalPath: cleanPhysical, Boundary: cleanBoundary}, nil
}

func selectPlacement(resolved ResolvedPath, resourceID string) (store.PathPlacement, error) {
	candidates := append([]store.PathPlacement(nil), resolved.Placements...)
	if len(candidates) == 0 || resourceID == resolved.DefaultPlacement.ResourceID {
		foundDefault := false
		for _, candidate := range candidates {
			if candidate.ID == resolved.DefaultPlacement.ID {
				foundDefault = true
				break
			}
		}
		if !foundDefault {
			candidates = append(candidates, resolved.DefaultPlacement)
		}
	}
	if resourceID != "" {
		for _, candidate := range candidates {
			if candidate.ResourceID == resourceID {
				return candidate, nil
			}
		}
		return store.PathPlacement{}, fmt.Errorf("no placement of %s is registered on resource %s", resolved.LogicalURI, resourceID)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return placementPriority(candidates[i]) < placementPriority(candidates[j])
	})
	if len(candidates) == 0 {
		return store.PathPlacement{}, fmt.Errorf("no placement is available for %s", resolved.LogicalURI)
	}
	return candidates[0], nil
}

func placementPriority(placement store.PathPlacement) int {
	switch placement.Role {
	case store.PlacementRoleAuthoritative:
		return 0
	case store.PlacementRoleReplica:
		return 1
	case store.PlacementRoleCache:
		return 2
	default:
		return 3
	}
}

func placementID(logicalURI, resourceID, physicalPath string) string {
	digest := sha256.Sum256([]byte(logicalURI + "\x00" + resourceID + "\x00" + physicalPath))
	return fmt.Sprintf("placement_%x", digest[:8])
}

func (s *Service) freshness(checkedAt *time.Time) string {
	if checkedAt == nil {
		return "unknown"
	}
	if s.ObservationTTL > 0 && s.now().Sub(*checkedAt) > s.ObservationTTL {
		return "stale"
	}
	return "fresh"
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func dereferencePlacement(placement *store.PathPlacement, err error) (store.PathPlacement, error) {
	if err != nil {
		return store.PathPlacement{}, err
	}
	if placement == nil {
		return store.PathPlacement{}, fmt.Errorf("placement disappeared after observation")
	}
	return *placement, nil
}
