package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
)

type PlanRequest struct {
	Source         string          `json:"source"`
	Destination    string          `json:"destination"`
	SourceRevision string          `json:"source_revision,omitempty"`
	Initiator      string          `json:"initiator,omitempty"`
	Verification   string          `json:"verification,omitempty"`
	Selection      []ManifestEntry `json:"selection,omitempty"`
}

type Endpoint struct {
	URI             string     `json:"uri"`
	LogicalURI      string     `json:"logical_uri,omitempty"`
	Scheme          string     `json:"scheme"`
	ResourceID      string     `json:"resource_id,omitempty"`
	ResourceName    string     `json:"resource_name,omitempty"`
	StorageTargetID string     `json:"storage_target_id,omitempty"`
	PlacementID     string     `json:"placement_id,omitempty"`
	PhysicalPath    string     `json:"physical_path"`
	Boundary        string     `json:"boundary,omitempty"`
	Role            string     `json:"role,omitempty"`
	ObservedState   string     `json:"observed_state,omitempty"`
	Freshness       string     `json:"freshness,omitempty"`
	Revision        string     `json:"revision,omitempty"`
	ManifestSHA256  string     `json:"manifest_sha256,omitempty"`
	Bytes           int64      `json:"bytes"`
	FileCount       int64      `json:"file_count,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
}

type Route struct {
	Initiator         string `json:"initiator"`
	CommandResourceID string `json:"command_resource_id,omitempty"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
}

type Blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Plan struct {
	PlanSHA256        string          `json:"plan_sha256"`
	Source            Endpoint        `json:"source"`
	Destination       Endpoint        `json:"destination"`
	Initiator         string          `json:"initiator,omitempty"`
	CommandResourceID string          `json:"command_resource_id,omitempty"`
	Fallback          []Route         `json:"fallback"`
	PayloadDirection  string          `json:"payload_direction"`
	LocalDataPath     bool            `json:"local_data_path"`
	Verification      string          `json:"verification"`
	StagingPath       string          `json:"staging_path"`
	FileCount         int64           `json:"file_count"`
	TotalBytes        int64           `json:"total_bytes"`
	AlreadySatisfied  bool            `json:"already_satisfied"`
	Selection         []ManifestEntry `json:"selection,omitempty"`
	Blockers          []Blocker       `json:"blockers"`
	GeneratedAt       time.Time       `json:"generated_at"`
	ExpiresAt         time.Time       `json:"expires_at"`
}

type Planner struct {
	Store    store.Store
	Files    *filespace.Service
	Now      func() time.Time
	PlanTTL  time.Duration
	RouteTTL time.Duration
}

func NewPlanner(db store.Store, files *filespace.Service) *Planner {
	return &Planner{Store: db, Files: files, Now: time.Now, PlanTTL: 5 * time.Minute, RouteTTL: 5 * time.Minute}
}

func (p *Planner) Build(ctx context.Context, request PlanRequest) (Plan, error) {
	if strings.TrimSpace(request.Source) == "" || strings.TrimSpace(request.Destination) == "" {
		return Plan{}, fmt.Errorf("transfer source and destination are required")
	}
	now := p.now()
	plan := Plan{
		PayloadDirection: "source_to_destination",
		Verification:     request.Verification,
		Fallback:         []Route{},
		Blockers:         []Blocker{},
		GeneratedAt:      now,
		ExpiresAt:        now.Add(p.PlanTTL),
	}
	if plan.Verification == "" {
		plan.Verification = "sha256"
	}
	if !oneOf(plan.Verification, "sha256", "manifest", "none") {
		return Plan{}, fmt.Errorf("unsupported verification %q", plan.Verification)
	}
	var err error
	plan.Source, err = p.resolveEndpoint(ctx, request.Source, true)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve source: %w", err)
	}
	plan.Destination, err = p.resolveEndpoint(ctx, request.Destination, false)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve destination: %w", err)
	}
	if len(request.Selection) == 0 && request.SourceRevision == "" && (plan.Source.Scheme != filespace.Scheme || plan.Source.Revision == "") {
		p.observeEndpoint(ctx, &plan.Source)
	}
	if plan.Destination.Scheme != filespace.Scheme && plan.Destination.Scheme != "local" {
		p.observeEndpoint(ctx, &plan.Destination)
	}
	if plan.Source.Scheme == filespace.Scheme && plan.Destination.Scheme != filespace.Scheme && plan.Destination.Scheme != "local" {
		plan.Destination.LogicalURI = plan.Source.URI
		plan.Destination.PlacementID = transferPlacementID(plan.Source.URI, plan.Destination.ResourceID, plan.Destination.PhysicalPath)
		if plan.Destination.StorageTargetID != "" {
			plan.Destination.Role = store.PlacementRoleReplica
		} else {
			plan.Destination.Role = store.PlacementRoleCache
		}
	}
	if len(request.Selection) > 0 {
		selection, revision, totalBytes, fileCount, selectionErr := NormalizeSelection(request.Selection)
		if selectionErr != nil {
			return Plan{}, selectionErr
		}
		plan.Selection, plan.TotalBytes, plan.FileCount = selection, totalBytes, fileCount
		if request.SourceRevision != "" && request.SourceRevision != revision {
			plan.Blockers = append(plan.Blockers, Blocker{Code: "selection_revision_mismatch", Message: "requested source revision does not match the normalized selection manifest"})
		}
		plan.Source.Revision = revision
	} else if request.SourceRevision != "" {
		if plan.Source.Revision != "" && plan.Source.Revision != request.SourceRevision {
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_revision_mismatch", Message: "requested source revision does not match the observed placement"})
		}
		plan.Source.Revision = request.SourceRevision
	}
	p.addSourceBlockers(&plan)
	p.addDestinationBlockers(&plan)
	plan.StagingPath = path.Join(path.Dir(plan.Destination.PhysicalPath), ".incoming-{transfer_id}")
	if len(plan.Selection) == 0 {
		plan.TotalBytes = plan.Source.Bytes
		plan.FileCount = plan.Source.FileCount
	}
	if plan.Source.Revision != "" && plan.Destination.ObservedState == store.PlacementObservedPresent && plan.Destination.Revision == plan.Source.Revision {
		plan.AlreadySatisfied = true
	} else {
		p.selectRoute(ctx, &plan, request.Initiator)
	}
	sort.Slice(plan.Blockers, func(i, j int) bool {
		if plan.Blockers[i].Code == plan.Blockers[j].Code {
			return plan.Blockers[i].Message < plan.Blockers[j].Message
		}
		return plan.Blockers[i].Code < plan.Blockers[j].Code
	})
	plan.PlanSHA256, err = hashPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (p *Planner) PersistentPlan(plan Plan) (store.TransferPlan, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return store.TransferPlan{}, err
	}
	return store.TransferPlan{
		PlanSHA256:             plan.PlanSHA256,
		Workspace:              workspaceFromURI(plan.Source.URI, plan.Destination.URI),
		SourceURI:              plan.Source.URI,
		DestinationURI:         plan.Destination.URI,
		SourcePlacementID:      plan.Source.PlacementID,
		DestinationPlacementID: plan.Destination.PlacementID,
		SourceRevision:         plan.Source.Revision,
		PlanJSON:               string(raw),
		ExpiresAt:              plan.ExpiresAt,
		CreatedAt:              plan.GeneratedAt,
	}, nil
}

func (p *Planner) resolveEndpoint(ctx context.Context, raw string, source bool) (Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, err
	}
	switch u.Scheme {
	case filespace.Scheme:
		return p.resolveLogicalEndpoint(ctx, raw, source)
	case "storage":
		return p.resolveStorageEndpoint(ctx, u)
	case "resource":
		return p.resolveResourceEndpoint(ctx, u)
	case "local":
		if u.RawQuery != "" || u.Fragment != "" || u.Path == "" || !path.IsAbs(u.Path) || unsafePath(u.Path) {
			return Endpoint{}, fmt.Errorf("local URI must contain a safe absolute path")
		}
		return Endpoint{URI: u.String(), Scheme: "local", ResourceID: "local", ResourceName: "Mac", PhysicalPath: path.Clean(u.Path), Boundary: "/"}, nil
	default:
		return Endpoint{}, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
}

func (p *Planner) resolveLogicalEndpoint(ctx context.Context, raw string, source bool) (Endpoint, error) {
	resolved, err := p.Files.Resolve(ctx, raw)
	if err != nil {
		return Endpoint{}, err
	}
	candidates := append([]store.PathPlacement(nil), resolved.Placements...)
	if len(candidates) == 0 {
		candidates = append(candidates, resolved.DefaultPlacement)
	}
	sort.Slice(candidates, func(i, j int) bool {
		pi, pj := endpointPlacementPriority(candidates[i]), endpointPlacementPriority(candidates[j])
		if pi != pj {
			return pi < pj
		}
		if candidates[i].ResourceID != candidates[j].ResourceID {
			return candidates[i].ResourceID < candidates[j].ResourceID
		}
		return candidates[i].PhysicalPath < candidates[j].PhysicalPath
	})
	selected := candidates[0]
	if source {
		for _, candidate := range candidates {
			if candidate.ObservedState == store.PlacementObservedPresent {
				selected = candidate
				break
			}
		}
	}
	resource, err := p.Store.GetResource(ctx, selected.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", selected.ResourceID)
		}
		return Endpoint{}, err
	}
	boundary := resource.RootDir
	if selected.StorageTargetID != "" {
		target, err := p.Store.GetStorageTarget(ctx, selected.StorageTargetID)
		if err != nil || target == nil {
			if err == nil {
				err = fmt.Errorf("storage target %s not found", selected.StorageTargetID)
			}
			return Endpoint{}, err
		}
		boundary = target.RootPath
	}
	return Endpoint{
		URI: resolved.LogicalURI, LogicalURI: resolved.LogicalURI, Scheme: filespace.Scheme, ResourceID: resource.ID, ResourceName: resource.Name,
		StorageTargetID: selected.StorageTargetID, PlacementID: selected.ID, PhysicalPath: selected.PhysicalPath,
		Boundary: boundary, Role: selected.Role, ObservedState: selected.ObservedState, Freshness: p.freshness(selected.CheckedAt),
		Revision: selected.Revision, ManifestSHA256: selected.ManifestSHA256, Bytes: selected.BytesPresent, CheckedAt: selected.CheckedAt,
	}, nil
}

func (p *Planner) resolveStorageEndpoint(ctx context.Context, u *url.URL) (Endpoint, error) {
	if p.Files == nil {
		return Endpoint{}, fmt.Errorf("file-space service is unavailable")
	}
	managed, err := p.Files.ResolveManagedURI(ctx, u.String())
	if err != nil {
		return Endpoint{}, err
	}
	if managed.RelativePath == "" {
		return Endpoint{}, fmt.Errorf("transfer endpoint must name a path below the storage root")
	}
	return Endpoint{URI: managed.URI, Scheme: managed.Scheme, ResourceID: managed.ResourceID, ResourceName: managed.ResourceName, StorageTargetID: managed.StorageTargetID, PhysicalPath: managed.PhysicalPath, Boundary: managed.Boundary, Role: managed.Role}, nil
}

func (p *Planner) resolveResourceEndpoint(ctx context.Context, u *url.URL) (Endpoint, error) {
	if p.Files == nil {
		return Endpoint{}, fmt.Errorf("file-space service is unavailable")
	}
	managed, err := p.Files.ResolveManagedURI(ctx, u.String())
	if err != nil {
		return Endpoint{}, err
	}
	if managed.RelativePath == "" {
		return Endpoint{}, fmt.Errorf("transfer endpoint must name a path below the resource root")
	}
	return Endpoint{URI: managed.URI, Scheme: managed.Scheme, ResourceID: managed.ResourceID, ResourceName: managed.ResourceName, PhysicalPath: managed.PhysicalPath, Boundary: managed.Boundary, Role: managed.Role}, nil
}

func (p *Planner) addSourceBlockers(plan *Plan) {
	if plan.Source.Scheme == filespace.Scheme {
		switch plan.Source.ObservedState {
		case store.PlacementObservedPresent:
		case store.PlacementObservedUnreachable:
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_unreachable", Message: "source placement could not be reached"})
		case store.PlacementObservedConflict:
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_conflict", Message: "source placement is in conflict"})
		default:
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_not_present", Message: "source placement is not freshly observed as present"})
		}
		if plan.Source.Freshness != "fresh" {
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_observation_stale", Message: "source placement observation is stale or missing"})
		}
	} else if plan.Source.CheckedAt != nil {
		switch plan.Source.ObservedState {
		case store.PlacementObservedPresent:
		case store.PlacementObservedMissing:
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_not_present", Message: "source path does not exist"})
		case store.PlacementObservedUnreachable:
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_unreachable", Message: "source resource could not be reached"})
		default:
			plan.Blockers = append(plan.Blockers, Blocker{Code: "source_not_observed", Message: "source path could not be verified"})
		}
	}
	if plan.Source.Revision == "" {
		code, message := "source_revision_missing", "source revision must be pinned before managed transfer"
		if plan.Source.Scheme != filespace.Scheme {
			code, message = "source_revision_unavailable", "source SHA-256 revision could not be discovered"
		}
		plan.Blockers = append(plan.Blockers, Blocker{Code: code, Message: message})
	}
}

func (p *Planner) addDestinationBlockers(plan *Plan) {
	if plan.Destination.ObservedState == store.PlacementObservedUnreachable {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "destination_unreachable", Message: "destination placement could not be reached"})
	}
	if plan.Destination.ObservedState == store.PlacementObservedUnknown && plan.Destination.CheckedAt != nil {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "destination_state_unknown", Message: "destination state could not be verified"})
	}
	if plan.Destination.ObservedState == store.PlacementObservedConflict {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "destination_conflict", Message: "destination placement is already in conflict"})
	}
	if plan.Destination.ObservedState == store.PlacementObservedPresent && plan.Destination.Revision != "" && plan.Source.Revision != "" && plan.Destination.Revision != plan.Source.Revision {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "destination_revision_conflict", Message: "destination already contains a different verified revision"})
	}
}

func (p *Planner) observeEndpoint(ctx context.Context, endpoint *Endpoint) {
	if p.Files == nil || p.Files.Remote == nil || endpoint == nil {
		if endpoint == nil || endpoint.Scheme != "local" {
			return
		}
	}
	now := p.now()
	endpoint.CheckedAt = &now
	endpoint.Freshness = "fresh"
	var entry filespace.RemoteEntry
	var err error
	if endpoint.Scheme == "local" {
		entry, err = filespace.StatLocalPath(endpoint.PhysicalPath, endpoint.Boundary)
	} else {
		stat, statErr := p.Files.StatURI(ctx, endpoint.URI, endpoint.ResourceID)
		entryBytes := stat.Location.EntryBytes
		if entryBytes == 0 {
			entryBytes = stat.Location.Bytes
		}
		entry = filespace.RemoteEntry{Exists: stat.Location.State == store.PlacementObservedPresent, Type: stat.Location.EntryType, Size: entryBytes, ModifiedNS: stat.Location.ModifiedNS}
		if stat.Location.State == store.PlacementObservedMissing {
			entry.Exists = false
		}
		if stat.Location.Error != "" {
			statErr = fmt.Errorf("%s", stat.Location.Error)
		}
		if stat.Location.State == store.PlacementObservedUnreachable {
			endpoint.ObservedState = store.PlacementObservedUnreachable
		}
		err = statErr
	}
	if err != nil {
		if endpoint.ObservedState != store.PlacementObservedUnreachable {
			endpoint.ObservedState = store.PlacementObservedUnknown
		}
		var remoteErr *filespace.RemoteError
		if errors.As(err, &remoteErr) && remoteErr.Kind == filespace.RemoteErrorUnreachable {
			endpoint.ObservedState = store.PlacementObservedUnreachable
		}
		return
	}
	if !entry.Exists {
		endpoint.ObservedState = store.PlacementObservedMissing
		return
	}
	var hashed filespace.HashResult
	if endpoint.Scheme == "local" {
		hashed, err = filespace.HashLocalPath(endpoint.PhysicalPath, endpoint.Boundary)
	} else {
		hashed, err = p.Files.HashURI(ctx, endpoint.URI, endpoint.ResourceID)
	}
	if err != nil {
		endpoint.ObservedState = store.PlacementObservedUnknown
		return
	}
	endpoint.ObservedState, endpoint.Revision, endpoint.ManifestSHA256, endpoint.Bytes, endpoint.FileCount = store.PlacementObservedPresent, hashed.Revision, hashed.ManifestSHA256, hashed.TotalBytes, hashed.FileCount
}

func (p *Planner) selectRoute(ctx context.Context, plan *Plan, requested string) {
	if requested == "" {
		requested = "auto"
	}
	if !oneOf(requested, "auto", "nas", "compute", "mac") {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "invalid_initiator", Message: "initiator must be auto, nas, compute, or mac"})
		return
	}
	if requested == "mac" || plan.Source.Scheme == "local" || plan.Destination.Scheme == "local" {
		plan.Initiator = "mac"
		plan.CommandResourceID = "local"
		plan.LocalDataPath = true
		return
	}
	if plan.Source.ResourceID == plan.Destination.ResourceID {
		plan.Initiator = "resource"
		plan.CommandResourceID = plan.Source.ResourceID
		return
	}
	storageEndpoint, computeEndpoint := plan.Source, plan.Destination
	if storageEndpoint.StorageTargetID == "" {
		storageEndpoint, computeEndpoint = plan.Destination, plan.Source
	}
	if storageEndpoint.StorageTargetID == "" {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "route_unverified", Message: "no managed storage edge describes this remote-to-remote transfer"})
		return
	}
	target, err := p.Store.GetStorageTarget(ctx, storageEndpoint.StorageTargetID)
	if err != nil || target == nil || target.Health == nil {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "route_health_missing", Message: "storage route health has not been observed"})
		return
	}
	var edge *store.StorageDataPlaneHealth
	for i := range target.Health.DataPlane {
		if target.Health.DataPlane[i].ResourceID == computeEndpoint.ResourceID {
			edge = &target.Health.DataPlane[i]
			break
		}
	}
	if edge == nil {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "route_health_missing", Message: "no storage route is registered for the compute resource"})
		return
	}
	if edge.CheckedAt.IsZero() || p.now().Sub(edge.CheckedAt) > p.RouteTTL {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "route_health_stale", Message: "storage route observation is stale"})
		return
	}
	routes := []Route{
		{Initiator: "nas", CommandResourceID: storageEndpoint.ResourceID, Status: edge.NASInitiated.Status, Reason: edge.NASInitiated.Error},
		{Initiator: "compute", CommandResourceID: computeEndpoint.ResourceID, Status: edge.ComputeInitiated.Status, Reason: edge.ComputeInitiated.Error},
	}
	if requested != "auto" {
		for _, route := range routes {
			if route.Initiator == requested && route.Status == store.StorageStatusHealthy {
				plan.Initiator, plan.CommandResourceID = route.Initiator, route.CommandResourceID
				return
			}
		}
		plan.Blockers = append(plan.Blockers, Blocker{Code: "initiator_unavailable", Message: "requested initiator is not healthy on the selected storage edge"})
		return
	}
	for _, route := range routes {
		if route.Status != store.StorageStatusHealthy {
			continue
		}
		if plan.Initiator == "" {
			plan.Initiator, plan.CommandResourceID = route.Initiator, route.CommandResourceID
			continue
		}
		plan.Fallback = append(plan.Fallback, route)
	}
	if plan.Initiator == "" {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "route_unavailable", Message: "no healthy initiator is available on the selected storage edge"})
	}
}

func hashPlan(plan Plan) (string, error) {
	source, destination := plan.Source, plan.Destination
	// Observation time is diagnostic metadata; state/revision and route health
	// determine the accepted plan identity.
	source.CheckedAt, destination.CheckedAt = nil, nil
	canonical := struct {
		Source            Endpoint        `json:"source"`
		Destination       Endpoint        `json:"destination"`
		Initiator         string          `json:"initiator"`
		CommandResourceID string          `json:"command_resource_id"`
		Fallback          []Route         `json:"fallback"`
		PayloadDirection  string          `json:"payload_direction"`
		LocalDataPath     bool            `json:"local_data_path"`
		Verification      string          `json:"verification"`
		StagingPath       string          `json:"staging_path"`
		FileCount         int64           `json:"file_count"`
		TotalBytes        int64           `json:"total_bytes"`
		AlreadySatisfied  bool            `json:"already_satisfied"`
		Selection         []ManifestEntry `json:"selection,omitempty"`
		Blockers          []Blocker       `json:"blockers"`
	}{source, destination, plan.Initiator, plan.CommandResourceID, plan.Fallback, plan.PayloadDirection, plan.LocalDataPath, plan.Verification, plan.StagingPath, plan.FileCount, plan.TotalBytes, plan.AlreadySatisfied, plan.Selection, plan.Blockers}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func transferPlacementID(logicalURI, resourceID, physicalPath string) string {
	digest := sha256.Sum256([]byte(logicalURI + "\x00" + resourceID + "\x00" + physicalPath))
	return fmt.Sprintf("placement_%x", digest[:8])
}

func physicalURIRelative(u *url.URL) (string, error) {
	if u.Host == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("physical URI requires a resource name and no user, port, query, or fragment")
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", err
	}
	decoded = strings.TrimPrefix(decoded, "/")
	if decoded == "" || unsafePath(decoded) {
		return "", fmt.Errorf("physical URI contains an unsafe or empty relative path")
	}
	return path.Clean(decoded), nil
}

func unsafePath(value string) bool {
	if strings.ContainsAny(value, "\\\x00\r\n") {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func endpointPlacementPriority(placement store.PathPlacement) int {
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

func (p *Planner) freshness(checkedAt *time.Time) string {
	if checkedAt == nil {
		return "unknown"
	}
	if p.RouteTTL > 0 && p.now().Sub(*checkedAt) > p.RouteTTL {
		return "stale"
	}
	return "fresh"
}

func (p *Planner) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func workspaceFromURI(values ...string) string {
	for _, value := range values {
		if logical, err := filespace.Parse(value); err == nil {
			return logical.Workspace
		}
	}
	return ""
}
