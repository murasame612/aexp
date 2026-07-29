package filespace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type ManifestEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

func NormalizeManifestSelection(input []ManifestEntry) ([]ManifestEntry, string, int64, int64, error) {
	if len(input) == 0 {
		return nil, "", 0, 0, nil
	}
	entries := make(map[string]ManifestEntry, len(input))
	for _, raw := range input {
		entry := raw
		entry.Path = path.Clean(strings.TrimSpace(entry.Path))
		entry.Type = strings.ToLower(strings.TrimSpace(entry.Type))
		if entry.Type == "" {
			entry.Type = "file"
		}
		if entry.Path == "" || entry.Path == "." || path.IsAbs(entry.Path) || unsafeManifestPath(entry.Path) {
			return nil, "", 0, 0, fmt.Errorf("selection path %q is unsafe", raw.Path)
		}
		if entry.Type != "file" && entry.Type != "directory" {
			return nil, "", 0, 0, fmt.Errorf("selection path %s has unsupported type %q", entry.Path, entry.Type)
		}
		if entry.Size < 0 {
			return nil, "", 0, 0, fmt.Errorf("selection path %s has a negative size", entry.Path)
		}
		if entry.Type == "file" {
			digest := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(entry.SHA256), "sha256:"))
			decoded, err := hex.DecodeString(digest)
			if err != nil || len(decoded) != sha256.Size {
				return nil, "", 0, 0, fmt.Errorf("selection path %s requires a full SHA-256", entry.Path)
			}
			entry.SHA256 = "sha256:" + digest
		} else {
			entry.SHA256, entry.Size = "", 0
		}
		if existing, ok := entries[entry.Path]; ok {
			if existing != entry {
				return nil, "", 0, 0, fmt.Errorf("selection path %s has conflicting entries", entry.Path)
			}
			continue
		}
		entries[entry.Path] = entry
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	for _, name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if existing, ok := entries[parent]; ok && existing.Type != "directory" {
				return nil, "", 0, 0, fmt.Errorf("selection path %s is both a file and a parent directory", parent)
			}
			entries[parent] = ManifestEntry{Path: parent, Type: "directory"}
		}
	}
	normalized := make([]ManifestEntry, 0, len(entries))
	for _, entry := range entries {
		normalized = append(normalized, entry)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Path < normalized[j].Path })
	revision, totalBytes, fileCount := ManifestRevision(normalized)
	return normalized, revision, totalBytes, fileCount, nil
}

func ManifestRevision(entries []ManifestEntry) (string, int64, int64) {
	manifest := sha256.New()
	var totalBytes, fileCount int64
	for _, entry := range entries {
		if entry.Type == "directory" {
			_, _ = fmt.Fprintf(manifest, "D  %s\n", entry.Path)
			continue
		}
		_, _ = fmt.Fprintf(manifest, "F %s  %d  %s\n", strings.TrimPrefix(entry.SHA256, "sha256:"), entry.Size, entry.Path)
		totalBytes += entry.Size
		fileCount++
	}
	return fmt.Sprintf("sha256:%x", manifest.Sum(nil)), totalBytes, fileCount
}

func StatLocalPath(physicalPath, boundary string) (RemoteEntry, error) {
	if err := EnsureLocalBoundary(physicalPath, boundary); err != nil {
		return RemoteEntry{}, err
	}
	info, err := os.Lstat(physicalPath)
	if errors.Is(err, os.ErrNotExist) {
		return RemoteEntry{Path: physicalPath, Exists: false}, nil
	}
	if err != nil {
		return RemoteEntry{}, err
	}
	kind := "other"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "file"
	}
	return RemoteEntry{Path: physicalPath, Name: info.Name(), Exists: true, Type: kind, Size: info.Size(), ModifiedNS: info.ModTime().UnixNano()}, nil
}

func HashLocalPath(root, boundary string) (HashResult, error) {
	if err := EnsureLocalBoundary(root, boundary); err != nil {
		return HashResult{}, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return HashResult{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return HashResult{}, fmt.Errorf("symlink is not a valid managed payload")
	}
	if info.Mode().IsRegular() {
		digest, err := hashLocalFile(root)
		if err != nil {
			return HashResult{}, err
		}
		return HashResult{Revision: "sha256:" + digest, FileCount: 1, TotalBytes: info.Size()}, nil
	}
	if !info.IsDir() {
		return HashResult{}, fmt.Errorf("unsupported payload type")
	}
	entries := make([]ManifestEntry, 0)
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not a valid managed payload: %s", current)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if strings.ContainsAny(relative, "\r\n") {
			return fmt.Errorf("newline in managed path")
		}
		if info.IsDir() {
			entries = append(entries, ManifestEntry{Path: relative, Type: "directory"})
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		digest, err := hashLocalFile(current)
		if err != nil {
			return err
		}
		entries = append(entries, ManifestEntry{Path: relative, Type: "file", SHA256: "sha256:" + digest, Size: info.Size()})
		return nil
	})
	if err != nil {
		return HashResult{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	revision, total, fileCount := ManifestRevision(entries)
	return HashResult{Revision: revision, ManifestSHA256: revision, FileCount: fileCount, TotalBytes: total}, nil
}

func EnsureLocalBoundary(value, boundary string) error {
	if boundary == "" {
		return fmt.Errorf("managed boundary is required")
	}
	boundaryPath, err := filepath.Abs(boundary)
	if err != nil {
		return err
	}
	boundaryPath, err = filepath.EvalSymlinks(boundaryPath)
	if err != nil {
		return err
	}
	valuePath, err := filepath.Abs(value)
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(valuePath); resolveErr == nil {
		valuePath = resolved
	} else if errors.Is(resolveErr, os.ErrNotExist) {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(valuePath))
		if parentErr == nil {
			valuePath = filepath.Join(parent, filepath.Base(valuePath))
		}
	}
	relative, err := filepath.Rel(boundaryPath, valuePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes managed boundary")
	}
	return nil
}

func hashLocalFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func unsafeManifestPath(value string) bool {
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
