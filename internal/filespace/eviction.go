package filespace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ziwu/aexp/internal/store"
)

type EvictionPlan struct {
	LogicalURI               string `json:"logical_uri"`
	SourcePlacementID        string `json:"source_placement_id"`
	SourceResourceID         string `json:"source_resource_id"`
	SourcePhysicalPath       string `json:"source_physical_path"`
	SourceRevision           string `json:"source_revision"`
	Bytes                    int64  `json:"bytes"`
	AuthoritativePlacementID string `json:"authoritative_placement_id"`
	AuthoritativeResourceID  string `json:"authoritative_resource_id"`
	PlanSHA256               string `json:"plan_sha256"`
}

func (s *Service) PlanEviction(ctx context.Context, rawURI, resourceID string) (EvictionPlan, error) {
	resolved, err := s.Resolve(ctx, rawURI)
	if err != nil {
		return EvictionPlan{}, err
	}
	source, err := selectPlacement(resolved, resourceID)
	if err != nil {
		return EvictionPlan{}, err
	}
	sourceResource, err := s.Store.GetResource(ctx, source.ResourceID)
	if err != nil || sourceResource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", source.ResourceID)
		}
		return EvictionPlan{}, err
	}
	sourceLocation, err := s.remoteLocation(ctx, sourceResource, source)
	if err != nil {
		return EvictionPlan{}, err
	}
	sourceEntry, err := s.Remote.Stat(ctx, sourceLocation)
	if err != nil {
		return EvictionPlan{}, fmt.Errorf("inspect eviction source: %w", err)
	}
	if !sourceEntry.Exists {
		return EvictionPlan{}, fmt.Errorf("eviction source is missing")
	}
	sourceHash, err := s.Remote.Hash(ctx, sourceLocation)
	if err != nil {
		return EvictionPlan{}, fmt.Errorf("hash eviction source: %w", err)
	}

	var alternate store.PathPlacement
	for _, candidate := range resolved.Placements {
		if candidate.ID == source.ID || candidate.Role != store.PlacementRoleAuthoritative || candidate.ObservedState != store.PlacementObservedPresent || candidate.Revision != sourceHash.Revision {
			continue
		}
		resource, getErr := s.Store.GetResource(ctx, candidate.ResourceID)
		if getErr != nil || resource == nil {
			continue
		}
		location, locationErr := s.remoteLocation(ctx, resource, candidate)
		if locationErr != nil {
			continue
		}
		entry, statErr := s.Remote.Stat(ctx, location)
		if statErr != nil || !entry.Exists {
			continue
		}
		hashed, hashErr := s.Remote.Hash(ctx, location)
		if hashErr == nil && hashed.Revision == sourceHash.Revision {
			alternate = candidate
			break
		}
	}
	if alternate.ID == "" {
		return EvictionPlan{}, fmt.Errorf("eviction refused: no other live verified authoritative placement matches revision %s", sourceHash.Revision)
	}
	plan := EvictionPlan{
		LogicalURI: resolved.LogicalURI, SourcePlacementID: source.ID, SourceResourceID: source.ResourceID,
		SourcePhysicalPath: source.PhysicalPath, SourceRevision: sourceHash.Revision, Bytes: sourceHash.TotalBytes,
		AuthoritativePlacementID: alternate.ID, AuthoritativeResourceID: alternate.ResourceID,
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return EvictionPlan{}, err
	}
	digest := sha256.Sum256(raw)
	plan.PlanSHA256 = "sha256:" + hex.EncodeToString(digest[:])
	return plan, nil
}

func (s *Service) Evict(ctx context.Context, rawURI, resourceID, expectedPlanSHA256 string) (EvictionPlan, error) {
	plan, err := s.PlanEviction(ctx, rawURI, resourceID)
	if err != nil {
		return EvictionPlan{}, err
	}
	if expectedPlanSHA256 == "" || expectedPlanSHA256 != plan.PlanSHA256 {
		return EvictionPlan{}, fmt.Errorf("eviction plan changed: expected %s, actual %s", expectedPlanSHA256, plan.PlanSHA256)
	}
	remover, ok := s.Remote.(RemoteRemover)
	if !ok {
		return EvictionPlan{}, fmt.Errorf("remote filesystem does not permit managed eviction")
	}
	placement, err := s.Store.GetPathPlacement(ctx, plan.SourcePlacementID)
	if err != nil || placement == nil {
		if err == nil {
			err = fmt.Errorf("source placement %s not found", plan.SourcePlacementID)
		}
		return EvictionPlan{}, err
	}
	resource, err := s.Store.GetResource(ctx, placement.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("source resource %s not found", placement.ResourceID)
		}
		return EvictionPlan{}, err
	}
	location, err := s.remoteLocation(ctx, resource, *placement)
	if err != nil {
		return EvictionPlan{}, err
	}
	if err := remover.RemoveVerified(ctx, location, plan.SourceRevision); err != nil {
		return EvictionPlan{}, err
	}
	placement.DesiredState = store.PlacementDesiredAbsent
	if err := s.Store.SavePathPlacement(ctx, placement); err != nil {
		return EvictionPlan{}, err
	}
	now := s.now()
	updated, err := s.Store.UpdatePathPlacementObservation(ctx, placement.ID, store.PlacementObservation{State: store.PlacementObservedMissing, Source: "managed_evict", CheckedAt: now, ObservedAt: &now})
	if err != nil {
		return EvictionPlan{}, err
	}
	if !updated {
		return EvictionPlan{}, fmt.Errorf("evicted payload but placement observation was superseded; refresh path state")
	}
	return plan, nil
}
