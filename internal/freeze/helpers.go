package freeze

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ziwu/aexp/internal/store"
)

func transition(ctx context.Context, db store.Store, freeze *store.RunFreeze, expected, next string) error {
	freeze.State, freeze.Stage = next, next
	freeze.ErrorCode, freeze.LastError = "", ""
	ok, err := db.UpdateRunFreezeIfState(ctx, freeze, expected)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("freeze %s state changed concurrently", freeze.ID)
	}
	return nil
}

func failFreeze(_ context.Context, db store.Store, freeze *store.RunFreeze, expected, code string, cause error) error {
	freeze.State, freeze.Stage = store.RunFreezeFailed, expected
	freeze.ErrorCode, freeze.LastError = code, cause.Error()
	if _, err := db.UpdateRunFreezeIfState(context.Background(), freeze, expected); err != nil {
		return err
	}
	return cause
}

func blockFreeze(_ context.Context, db store.Store, freeze *store.RunFreeze, expected, code string, cause error) error {
	freeze.State, freeze.Stage = store.RunFreezeBlocked, expected
	freeze.ErrorCode, freeze.LastError = code, cause.Error()
	if _, err := db.UpdateRunFreezeIfState(context.Background(), freeze, expected); err != nil {
		return err
	}
	return nil
}

func storagePrefix(uri, name string) (string, error) {
	prefix := "storage://" + name + "/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("destination must start with %s", prefix)
	}
	value := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(uri, prefix)))
	if value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("unsafe storage prefix")
	}
	return value, nil
}

func collectDerived(freezeID, rawManifestSHA256, workspace string, patterns []string) ([]store.RunFreezeFile, error) {
	out := []store.RunFreezeFile{}
	root := filepath.Join(workspace, "derived")
	err := filepath.WalkDir(root, func(physicalPath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(workspace, physicalPath)
		relative = filepath.ToSlash(relative)
		if !matchesAny(relative, patterns) || strings.HasSuffix(relative, ".provenance.json") {
			return nil
		}
		sidecar := physicalPath + ".provenance.json"
		if _, err := os.Stat(sidecar); err != nil {
			return fmt.Errorf("missing provenance sidecar for %s", relative)
		}
		var provenance map[string]interface{}
		raw, err := os.ReadFile(sidecar)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &provenance); err != nil {
			return fmt.Errorf("invalid provenance sidecar for %s: %w", relative, err)
		}
		if provenance["freeze_id"] != freezeID {
			return fmt.Errorf("provenance sidecar for %s references a different freeze", relative)
		}
		if provenance["raw_manifest_sha256"] != rawManifestSHA256 {
			return fmt.Errorf("provenance sidecar for %s references a different raw manifest", relative)
		}
		hash, size, err := hashPath(physicalPath)
		if err != nil {
			return err
		}
		out = append(out, store.RunFreezeFile{ID: fileID(freezeID, "derived", relative), FreezeID: freezeID, Kind: "derived", Role: derivedRole(relative), RelativePath: relative, SourceURI: physicalPath, FrozenURI: physicalPath, SHA256: hash, Size: size, Required: true})
		sidecarHash, sidecarSize, err := hashPath(sidecar)
		if err != nil {
			return err
		}
		sidecarRelative := relative + ".provenance.json"
		out = append(out, store.RunFreezeFile{ID: fileID(freezeID, "derived", sidecarRelative), FreezeID: freezeID, Kind: "derived", Role: "provenance", RelativePath: sidecarRelative, SourceURI: sidecar, FrozenURI: sidecar, SHA256: sidecarHash, Size: sidecarSize, Required: true})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("aggregate produced no declared outputs")
	}
	return out, nil
}

func hashPath(physicalPath string) (string, int64, error) {
	file, err := os.Open(physicalPath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), info.Size(), nil
}

func freezeEnv(env []string, id, workspace string) []string {
	return append(env, "AEXP_FREEZE_ID="+id, "AEXP_FREEZE_DIR="+workspace, "AEXP_RAW_DIR="+filepath.Join(workspace, "raw"), "AEXP_DERIVED_DIR="+filepath.Join(workspace, "derived"))
}

func fileID(freezeID, kind, relativePath string) string {
	digest := sha256.Sum256([]byte(freezeID + "\x00" + kind + "\x00" + relativePath))
	return "ffile_" + hex.EncodeToString(digest[:8])
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func jsonString(value interface{}) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func tail(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[len(value)-max:]
}

func derivedRole(value string) string {
	switch strings.ToLower(filepath.Ext(value)) {
	case ".csv", ".tsv":
		return "table"
	case ".png", ".pdf", ".svg":
		return "figure"
	default:
		return "derived"
	}
}
