package filespace

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

// ManagedLocation is the resolved, boundary-checked physical location behind
// a storage:// or resource:// URI. Resource is intentionally excluded from
// JSON so callers receive stable identities rather than connection secrets.
type ManagedLocation struct {
	URI             string          `json:"uri"`
	Scheme          string          `json:"scheme"`
	ResourceID      string          `json:"resource_id"`
	ResourceName    string          `json:"resource_name"`
	StorageTargetID string          `json:"storage_target_id,omitempty"`
	RelativePath    string          `json:"relative_path,omitempty"`
	PhysicalPath    string          `json:"physical_path"`
	Boundary        string          `json:"-"`
	Role            string          `json:"role"`
	Resource        *store.Resource `json:"-"`
}

type PathLocation struct {
	URI             string     `json:"uri"`
	Scheme          string     `json:"scheme"`
	ResourceID      string     `json:"resource_id"`
	ResourceName    string     `json:"resource_name"`
	StorageTargetID string     `json:"storage_target_id,omitempty"`
	PhysicalPath    string     `json:"physical_path,omitempty"`
	Role            string     `json:"role"`
	State           string     `json:"state"`
	Freshness       string     `json:"freshness"`
	Revision        string     `json:"revision,omitempty"`
	ManifestSHA256  string     `json:"manifest_sha256,omitempty"`
	Bytes           int64      `json:"bytes,omitempty"`
	EntryBytes      int64      `json:"entry_bytes,omitempty"`
	EntryType       string     `json:"type,omitempty"`
	ModifiedNS      int64      `json:"modified_ns,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	Error           string     `json:"error,omitempty"`
}

type PathStatResult struct {
	Location PathLocation `json:"location"`
}

type PathListEntry struct {
	Name       string `json:"name"`
	Type       string `json:"type,omitempty"`
	Size       int64  `json:"size,omitempty"`
	ModifiedNS int64  `json:"modified_ns,omitempty"`
}

type PathListResult struct {
	URI        string          `json:"uri"`
	Location   PathLocation    `json:"location"`
	Entries    []PathListEntry `json:"entries"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// ResolveManagedURI resolves a storage:// or resource:// URI without touching
// the remote filesystem. Empty paths are allowed for read-only root browsing;
// transfer planning keeps its stricter non-empty payload rule.
func (s *Service) ResolveManagedURI(ctx context.Context, rawURI string) (ManagedLocation, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return ManagedLocation{}, err
	}
	relative, err := managedURIRelative(u)
	if err != nil {
		return ManagedLocation{}, err
	}
	switch u.Scheme {
	case "storage":
		target, err := s.Store.GetStorageTargetByName(ctx, u.Host)
		if err != nil || target == nil {
			if err == nil {
				err = fmt.Errorf("storage target %s not found", u.Host)
			}
			return ManagedLocation{}, err
		}
		resource, err := s.Store.GetResource(ctx, target.ResourceID)
		if err != nil || resource == nil {
			if err == nil {
				err = fmt.Errorf("storage resource %s not found", target.ResourceID)
			}
			return ManagedLocation{}, err
		}
		physical := path.Clean(target.RootPath)
		if relative != "" {
			physical = path.Join(physical, relative)
		}
		return ManagedLocation{URI: u.String(), Scheme: u.Scheme, ResourceID: resource.ID, ResourceName: resource.Name, StorageTargetID: target.ID, RelativePath: relative, PhysicalPath: physical, Boundary: target.RootPath, Role: "primary", Resource: resource}, nil
	case "resource":
		resource, err := s.Store.GetResourceByName(ctx, u.Host)
		if err != nil || resource == nil {
			if err == nil {
				err = fmt.Errorf("resource %s not found", u.Host)
			}
			return ManagedLocation{}, err
		}
		physical := path.Clean(resource.RootDir)
		if relative != "" {
			physical = path.Join(physical, relative)
		}
		return ManagedLocation{URI: u.String(), Scheme: u.Scheme, ResourceID: resource.ID, ResourceName: resource.Name, RelativePath: relative, PhysicalPath: physical, Boundary: resource.RootDir, Role: "cache", Resource: resource}, nil
	default:
		return ManagedLocation{}, fmt.Errorf("unsupported managed URI scheme %q", u.Scheme)
	}
}

func (s *Service) StatURI(ctx context.Context, rawURI, resourceID string) (PathStatResult, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return PathStatResult{}, err
	}
	if u.Scheme == Scheme {
		result, err := s.Inspect(ctx, rawURI, resourceID)
		if err != nil {
			return PathStatResult{}, err
		}
		location, err := s.pathLocationFromPlacement(ctx, result.Placement.PathPlacement)
		if err != nil {
			return PathStatResult{}, err
		}
		location.Freshness = result.Placement.Freshness
		return PathStatResult{Location: location}, nil
	}
	managed, err := s.ResolveManagedURI(ctx, rawURI)
	if err != nil {
		return PathStatResult{}, err
	}
	if resourceID != "" && resourceID != managed.ResourceID {
		return PathStatResult{}, fmt.Errorf("URI resolves to resource %s, not %s", managed.ResourceID, resourceID)
	}
	if s.Remote == nil {
		return PathStatResult{}, fmt.Errorf("remote filesystem is unavailable")
	}
	now := s.now()
	location := PathLocation{URI: managed.URI, Scheme: managed.Scheme, ResourceID: managed.ResourceID, ResourceName: managed.ResourceName, StorageTargetID: managed.StorageTargetID, PhysicalPath: managed.PhysicalPath, Role: managed.Role, State: store.PlacementObservedUnknown, Freshness: "fresh", CheckedAt: &now}
	entry, statErr := s.Remote.Stat(ctx, RemoteLocation{Resource: managed.Resource, PhysicalPath: managed.PhysicalPath, Boundary: managed.Boundary})
	if statErr != nil {
		location.Error = statErr.Error()
		var remoteErr *RemoteError
		if errors.As(statErr, &remoteErr) && remoteErr.Kind == RemoteErrorUnreachable {
			location.State = store.PlacementObservedUnreachable
		}
		return PathStatResult{Location: location}, nil
	}
	location.EntryType, location.ModifiedNS, location.EntryBytes = entry.Type, entry.ModifiedNS, entry.Size
	if entry.Exists {
		location.State = store.PlacementObservedPresent
	} else {
		location.State = store.PlacementObservedMissing
	}
	return PathStatResult{Location: location}, nil
}

func (s *Service) ListURI(ctx context.Context, rawURI, resourceID, cursor string, limit int) (PathListResult, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return PathListResult{}, err
	}
	var result ListResult
	var location PathLocation
	if u.Scheme == Scheme {
		resolved, err := s.Resolve(ctx, rawURI)
		if err != nil {
			return PathListResult{}, err
		}
		placement, err := selectPlacement(resolved, resourceID)
		if err != nil {
			return PathListResult{}, err
		}
		location, err = s.pathLocationFromPlacement(ctx, placement)
		if err != nil {
			return PathListResult{}, err
		}
		result, err = s.List(ctx, rawURI, resourceID, cursor, limit)
		if err != nil {
			return PathListResult{}, err
		}
	} else {
		managed, err := s.ResolveManagedURI(ctx, rawURI)
		if err != nil {
			return PathListResult{}, err
		}
		if resourceID != "" && resourceID != managed.ResourceID {
			return PathListResult{}, fmt.Errorf("URI resolves to resource %s, not %s", managed.ResourceID, resourceID)
		}
		if s.Remote == nil {
			return PathListResult{}, fmt.Errorf("remote filesystem is unavailable")
		}
		location = PathLocation{URI: managed.URI, Scheme: managed.Scheme, ResourceID: managed.ResourceID, ResourceName: managed.ResourceName, StorageTargetID: managed.StorageTargetID, PhysicalPath: managed.PhysicalPath, Role: managed.Role, State: store.PlacementObservedUnknown, Freshness: "unknown"}
		result, err = s.Remote.List(ctx, RemoteLocation{Resource: managed.Resource, PhysicalPath: managed.PhysicalPath, Boundary: managed.Boundary}, cursor, limit)
		if err != nil {
			return PathListResult{}, err
		}
	}
	entries := make([]PathListEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}
		entries = append(entries, PathListEntry{Name: entry.Name, Type: entry.Type, Size: entry.Size, ModifiedNS: entry.ModifiedNS})
	}
	now := s.now()
	location.State, location.Freshness, location.CheckedAt = store.PlacementObservedPresent, "fresh", &now
	return PathListResult{URI: rawURI, Location: location, Entries: entries, NextCursor: result.NextCursor}, nil
}

func (s *Service) HashURI(ctx context.Context, rawURI, resourceID string) (HashResult, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return HashResult{}, err
	}
	if u.Scheme == Scheme {
		return s.Hash(ctx, rawURI, resourceID)
	}
	managed, err := s.ResolveManagedURI(ctx, rawURI)
	if err != nil {
		return HashResult{}, err
	}
	if resourceID != "" && resourceID != managed.ResourceID {
		return HashResult{}, fmt.Errorf("URI resolves to resource %s, not %s", managed.ResourceID, resourceID)
	}
	if s.Remote == nil {
		return HashResult{}, fmt.Errorf("remote filesystem is unavailable")
	}
	return s.Remote.Hash(ctx, RemoteLocation{Resource: managed.Resource, PhysicalPath: managed.PhysicalPath, Boundary: managed.Boundary})
}

func (s *Service) LocationsURI(ctx context.Context, rawURI string) ([]PathLocation, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return nil, err
	}
	if u.Scheme != Scheme {
		stat, err := s.StatURI(ctx, rawURI, "")
		if err != nil {
			return nil, err
		}
		return []PathLocation{stat.Location}, nil
	}
	placements, err := s.Locate(ctx, rawURI)
	if err != nil {
		return nil, err
	}
	locations := make([]PathLocation, 0, len(placements))
	for _, placement := range placements {
		location, err := s.pathLocationFromPlacement(ctx, placement.PathPlacement)
		if err != nil {
			return nil, err
		}
		location.Freshness = placement.Freshness
		locations = append(locations, location)
	}
	return locations, nil
}

func (s *Service) pathLocationFromPlacement(ctx context.Context, placement store.PathPlacement) (PathLocation, error) {
	resource, err := s.Store.GetResource(ctx, placement.ResourceID)
	if err != nil || resource == nil {
		if err == nil {
			err = fmt.Errorf("resource %s not found", placement.ResourceID)
		}
		return PathLocation{}, err
	}
	role := placement.Role
	if role == store.PlacementRoleAuthoritative {
		role = "primary"
	}
	return PathLocation{URI: placement.LogicalURI, Scheme: Scheme, ResourceID: resource.ID, ResourceName: resource.Name, StorageTargetID: placement.StorageTargetID, PhysicalPath: placement.PhysicalPath, Role: role, State: placement.ObservedState, Freshness: s.freshness(placement.CheckedAt), Revision: placement.Revision, ManifestSHA256: placement.ManifestSHA256, Bytes: placement.BytesPresent, CheckedAt: placement.CheckedAt, Error: placement.ObservationError}, nil
}

func managedURIRelative(u *url.URL) (string, error) {
	if u.Host == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("managed URI requires a resource name and no user, port, query, or fragment")
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", err
	}
	decoded = strings.TrimPrefix(decoded, "/")
	if strings.ContainsAny(decoded, "\\\x00\r\n") {
		return "", fmt.Errorf("managed URI contains an unsafe path")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == ".." {
			return "", fmt.Errorf("managed URI contains an unsafe path")
		}
	}
	if decoded == "" {
		return "", nil
	}
	return path.Clean(decoded), nil
}
