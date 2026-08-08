package portability

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

const BundleSchemaVersion = "aexp-portability-bundle-v1"

type BundleFile struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	EntityID     string `json:"entity_id,omitempty"`
	OriginalPath string `json:"original_path,omitempty"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
}

type BundleManifest struct {
	SchemaVersion string       `json:"schema_version"`
	CreatedAt     time.Time    `json:"created_at"`
	DatabasePath  string       `json:"database_path"`
	Audit         Report       `json:"audit"`
	Files         []BundleFile `json:"files"`
	Limitations   []string     `json:"limitations"`
}

type ExportOptions struct {
	DatabasePath    string
	AttachmentsRoot string
	OutputPath      string
	Now             func() time.Time
}

type ExportResult struct {
	BundlePath   string         `json:"bundle_path"`
	BundleSHA256 string         `json:"bundle_sha256"`
	Manifest     BundleManifest `json:"manifest"`
}

type RestoreOptions struct {
	BundlePath  string
	Destination string
	DryRun      bool
	Mappings    []store.PathPrefixMapping
}

type RestoreReport struct {
	SchemaVersion string                          `json:"schema_version"`
	Status        string                          `json:"status"`
	DryRun        bool                            `json:"dry_run"`
	Destination   string                          `json:"destination,omitempty"`
	BundleSHA256  string                          `json:"bundle_sha256"`
	FilesVerified int                             `json:"files_verified"`
	DBIntegrity   string                          `json:"database_integrity"`
	Rewrite       store.PortabilityRewriteSummary `json:"rewrite"`
	Audit         Report                          `json:"audit"`
	Limitations   []string                        `json:"limitations"`
}

func Export(ctx context.Context, options ExportOptions) (ExportResult, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	databasePath, err := filepath.Abs(options.DatabasePath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve database path: %w", err)
	}
	attachmentsRoot, err := filepath.Abs(options.AttachmentsRoot)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve attachments root: %w", err)
	}
	outputPath, err := filepath.Abs(options.OutputPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve bundle output: %w", err)
	}
	if _, err := os.Stat(outputPath); err == nil {
		return ExportResult{}, fmt.Errorf("bundle output already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return ExportResult{}, fmt.Errorf("stat bundle output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return ExportResult{}, fmt.Errorf("create bundle output directory: %w", err)
	}

	readonly, err := store.OpenSQLiteReadOnly(databasePath)
	if err != nil {
		return ExportResult{}, err
	}
	audit, auditErr := (Service{Store: readonly, DatabasePath: databasePath, AttachmentsRoot: attachmentsRoot, Now: now}).Audit(ctx)
	readonly.Close()
	if auditErr != nil {
		return ExportResult{}, auditErr
	}
	if audit.Summary.BlockingFindings > 0 {
		return ExportResult{}, fmt.Errorf("portability export blocked by %d audit finding(s)", audit.Summary.BlockingFindings)
	}

	workingDir, err := os.MkdirTemp(filepath.Dir(outputPath), ".aexp-portability-export-")
	if err != nil {
		return ExportResult{}, fmt.Errorf("create export workspace: %w", err)
	}
	defer os.RemoveAll(workingDir)
	databaseMember := filepath.ToSlash(filepath.Join("database", "aexp.db"))
	snapshotPath := filepath.Join(workingDir, filepath.FromSlash(databaseMember))
	if err := store.SnapshotSQLite(databasePath, snapshotPath); err != nil {
		return ExportResult{}, err
	}
	snapshot, err := store.NewSQLite(snapshotPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("open snapshot for portability preparation: %w", err)
	}
	if _, err := snapshot.PreparePortabilityCopy(ctx, nil, nil); err != nil {
		snapshot.Close()
		return ExportResult{}, err
	}
	if err := snapshot.Close(); err != nil {
		return ExportResult{}, fmt.Errorf("close prepared snapshot: %w", err)
	}

	manifest := BundleManifest{
		SchemaVersion: BundleSchemaVersion,
		CreatedAt:     now().UTC(),
		DatabasePath:  databaseMember,
		Audit:         audit,
		Files:         []BundleFile{},
		Limitations: []string{
			"remote artifacts and datasets are referenced but not copied",
			"project working trees are not copied",
			"SSH credentials and reachability must be rebound after import",
			"active Runs are preserved as records but are not resumed",
		},
	}
	databaseFile, err := describeBundleFile(snapshotPath, databaseMember, "database", "", databasePath)
	if err != nil {
		return ExportResult{}, err
	}
	manifest.Files = append(manifest.Files, databaseFile)

	for _, ref := range audit.Paths {
		if ref.EntityType != "attachment" || ref.Field != "local_path" {
			continue
		}
		member := filepath.ToSlash(filepath.Join("attachments", safeBundleName(ref.EntityID), safeBundleName(filepath.Base(ref.Path))))
		destination := filepath.Join(workingDir, filepath.FromSlash(member))
		if err := copyRegularFile(ref.Path, destination); err != nil {
			return ExportResult{}, fmt.Errorf("copy attachment %s: %w", ref.EntityID, err)
		}
		file, err := describeBundleFile(destination, member, "attachment", ref.EntityID, ref.Path)
		if err != nil {
			return ExportResult{}, err
		}
		manifest.Files = append(manifest.Files, file)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ExportResult{}, fmt.Errorf("encode bundle manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	manifestPath := filepath.Join(workingDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		return ExportResult{}, fmt.Errorf("write bundle manifest: %w", err)
	}

	temporaryArchive := filepath.Join(workingDir, "bundle.tar.gz")
	if err := writeTarGz(temporaryArchive, workingDir, append([]string{"manifest.json"}, bundleFilePaths(manifest.Files)...)); err != nil {
		return ExportResult{}, err
	}
	if err := os.Rename(temporaryArchive, outputPath); err != nil {
		return ExportResult{}, fmt.Errorf("publish portability bundle: %w", err)
	}
	if err := os.Chmod(outputPath, 0o600); err != nil {
		return ExportResult{}, fmt.Errorf("secure portability bundle: %w", err)
	}
	bundleHash, _, err := hashFile(outputPath)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{BundlePath: outputPath, BundleSHA256: bundleHash, Manifest: manifest}, nil
}

func Restore(ctx context.Context, options RestoreOptions) (RestoreReport, error) {
	bundlePath, err := filepath.Abs(options.BundlePath)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("resolve bundle path: %w", err)
	}
	bundleHash, _, err := hashFile(bundlePath)
	if err != nil {
		return RestoreReport{}, err
	}
	var workspace, finalDestination string
	if options.DryRun {
		workspace, err = os.MkdirTemp("", "aexp-portability-validate-")
		if err != nil {
			return RestoreReport{}, fmt.Errorf("create validation workspace: %w", err)
		}
		defer os.RemoveAll(workspace)
	} else {
		if strings.TrimSpace(options.Destination) == "" {
			return RestoreReport{}, fmt.Errorf("import destination is required")
		}
		finalDestination, err = filepath.Abs(options.Destination)
		if err != nil {
			return RestoreReport{}, fmt.Errorf("resolve import destination: %w", err)
		}
		if _, err := os.Stat(finalDestination); err == nil {
			return RestoreReport{}, fmt.Errorf("import destination already exists: %s", finalDestination)
		} else if !os.IsNotExist(err) {
			return RestoreReport{}, fmt.Errorf("stat import destination: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(finalDestination), 0o700); err != nil {
			return RestoreReport{}, fmt.Errorf("create import parent: %w", err)
		}
		workspace, err = os.MkdirTemp(filepath.Dir(finalDestination), ".aexp-portability-import-")
		if err != nil {
			return RestoreReport{}, fmt.Errorf("create import workspace: %w", err)
		}
		defer os.RemoveAll(workspace)
	}

	if err := extractTarGz(bundlePath, workspace); err != nil {
		return RestoreReport{}, err
	}
	manifest, err := readBundleManifest(filepath.Join(workspace, "manifest.json"))
	if err != nil {
		return RestoreReport{}, err
	}
	if manifest.SchemaVersion != BundleSchemaVersion {
		return RestoreReport{}, fmt.Errorf("unsupported portability bundle schema %q", manifest.SchemaVersion)
	}
	if err := validateBundleManifest(manifest); err != nil {
		return RestoreReport{}, err
	}
	if err := verifyBundleFiles(workspace, manifest.Files); err != nil {
		return RestoreReport{}, err
	}
	if err := verifyBundleContents(workspace, manifest.Files); err != nil {
		return RestoreReport{}, err
	}
	databasePath, err := safeBundlePath(workspace, manifest.DatabasePath)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("invalid database member: %w", err)
	}
	db, err := store.NewSQLite(databasePath)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("open imported database: %w", err)
	}
	attachmentPaths := map[string]string{}
	restoredRoot := workspace
	if !options.DryRun {
		restoredRoot = finalDestination
	}
	for _, file := range manifest.Files {
		if file.Kind != "attachment" || file.EntityID == "" {
			continue
		}
		path, pathErr := safeBundlePath(restoredRoot, file.Path)
		if pathErr != nil {
			db.Close()
			return RestoreReport{}, pathErr
		}
		attachmentPaths[file.EntityID] = path
	}
	rewrite, err := db.PreparePortabilityCopy(ctx, attachmentPaths, options.Mappings)
	if err != nil {
		db.Close()
		return RestoreReport{}, err
	}
	if err := db.IntegrityCheck(ctx); err != nil {
		db.Close()
		return RestoreReport{}, err
	}
	auditDatabasePath := databasePath
	auditAttachmentsRoot := filepath.Join(workspace, "attachments")
	var auditStat func(string) (os.FileInfo, error)
	if !options.DryRun {
		auditDatabasePath, err = safeBundlePath(finalDestination, manifest.DatabasePath)
		if err != nil {
			db.Close()
			return RestoreReport{}, err
		}
		auditAttachmentsRoot = filepath.Join(finalDestination, "attachments")
		auditStat = func(path string) (os.FileInfo, error) {
			rel, relErr := filepath.Rel(finalDestination, filepath.Clean(path))
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return os.Stat(filepath.Join(workspace, rel))
			}
			return os.Stat(path)
		}
	}
	audit, err := (Service{Store: db, DatabasePath: auditDatabasePath, AttachmentsRoot: auditAttachmentsRoot, Stat: auditStat}).Audit(ctx)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return RestoreReport{}, fmt.Errorf("audit imported database: %w", err)
	}
	report := RestoreReport{
		SchemaVersion: BundleSchemaVersion,
		Status:        "valid",
		DryRun:        options.DryRun,
		BundleSHA256:  bundleHash,
		FilesVerified: len(manifest.Files),
		DBIntegrity:   "ok",
		Rewrite:       rewrite,
		Audit:         audit,
		Limitations:   manifest.Limitations,
	}
	if audit.Summary.BlockingFindings > 0 || audit.Summary.Warnings > 0 {
		report.Status = "valid_with_findings"
	}
	if !options.DryRun {
		if err := os.Rename(workspace, finalDestination); err != nil {
			return RestoreReport{}, fmt.Errorf("publish imported workspace: %w", err)
		}
		report.Destination = finalDestination
	}
	return report, nil
}

func describeBundleFile(localPath, memberPath, kind, entityID, originalPath string) (BundleFile, error) {
	digest, size, err := hashFile(localPath)
	if err != nil {
		return BundleFile{}, err
	}
	return BundleFile{Path: memberPath, Kind: kind, EntityID: entityID, OriginalPath: originalPath, SHA256: digest, Size: size}, nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", path, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

func copyRegularFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func writeTarGz(outputPath, root string, members []string) error {
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create bundle archive: %w", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	closeAll := func() error {
		return errors.Join(tarWriter.Close(), gzipWriter.Close(), file.Close())
	}
	for _, member := range members {
		localPath, err := safeBundlePath(root, member)
		if err != nil {
			closeAll()
			return err
		}
		info, err := os.Stat(localPath)
		if err != nil {
			closeAll()
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			closeAll()
			return err
		}
		header.Name = filepath.ToSlash(member)
		header.Mode = 0o600
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err := tarWriter.WriteHeader(header); err != nil {
			closeAll()
			return err
		}
		input, err := os.Open(localPath)
		if err != nil {
			closeAll()
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			closeAll()
			return errors.Join(copyErr, closeErr)
		}
	}
	if err := closeAll(); err != nil {
		return fmt.Errorf("close bundle archive: %w", err)
	}
	return nil
}

func extractTarGz(bundlePath, destination string) error {
	file, err := os.Open(bundlePath)
	if err != nil {
		return fmt.Errorf("open portability bundle: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read portability bundle gzip: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	seen := map[string]struct{}{}
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read portability bundle: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("bundle member %q is not a regular file", header.Name)
		}
		cleanName := filepath.ToSlash(filepath.Clean(header.Name))
		if _, exists := seen[cleanName]; exists {
			return fmt.Errorf("duplicate bundle member %q", cleanName)
		}
		seen[cleanName] = struct{}{}
		target, err := safeBundlePath(destination, cleanName)
		if err != nil {
			return fmt.Errorf("unsafe bundle member %q: %w", header.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, tarReader)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
	}
	return nil
}

func readBundleManifest(path string) (BundleManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BundleManifest{}, fmt.Errorf("read bundle manifest: %w", err)
	}
	var manifest BundleManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return BundleManifest{}, fmt.Errorf("decode bundle manifest: %w", err)
	}
	return manifest, nil
}

func verifyBundleFiles(root string, files []BundleFile) error {
	seen := map[string]struct{}{}
	for _, expected := range files {
		if _, exists := seen[expected.Path]; exists {
			return fmt.Errorf("manifest contains duplicate file %q", expected.Path)
		}
		seen[expected.Path] = struct{}{}
		path, err := safeBundlePath(root, expected.Path)
		if err != nil {
			return err
		}
		digest, size, err := hashFile(path)
		if err != nil {
			return err
		}
		if digest != expected.SHA256 || size != expected.Size {
			return fmt.Errorf("bundle checksum mismatch for %s", expected.Path)
		}
	}
	return nil
}

func validateBundleManifest(manifest BundleManifest) error {
	if manifest.CreatedAt.IsZero() {
		return fmt.Errorf("bundle manifest has no creation time")
	}
	databaseEntries := 0
	attachmentIDs := map[string]struct{}{}
	for _, file := range manifest.Files {
		if file.Path == manifest.DatabasePath && file.Kind == "database" {
			databaseEntries++
		}
		if !strings.HasPrefix(file.SHA256, "sha256:") || len(strings.TrimPrefix(file.SHA256, "sha256:")) != 64 || file.Size < 0 {
			return fmt.Errorf("invalid checksum metadata for %s", file.Path)
		}
		if file.Kind == "attachment" {
			if file.EntityID == "" {
				return fmt.Errorf("attachment bundle member %s has no entity id", file.Path)
			}
			if _, exists := attachmentIDs[file.EntityID]; exists {
				return fmt.Errorf("duplicate attachment entity id %s", file.EntityID)
			}
			attachmentIDs[file.EntityID] = struct{}{}
		}
	}
	if databaseEntries != 1 {
		return fmt.Errorf("bundle manifest must contain exactly one database entry")
	}
	return nil
}

func verifyBundleContents(root string, files []BundleFile) error {
	expected := map[string]struct{}{"manifest.json": {}}
	for _, file := range files {
		expected[filepath.ToSlash(filepath.Clean(file.Path))] = struct{}{}
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := expected[rel]; !ok {
			return fmt.Errorf("unexpected bundle member %s", rel)
		}
		return nil
	})
}

func safeBundlePath(root, member string) (string, error) {
	member = filepath.FromSlash(strings.TrimSpace(member))
	if member == "" || filepath.IsAbs(member) {
		return "", fmt.Errorf("bundle path must be relative")
	}
	clean := filepath.Clean(member)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle path escapes root")
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle path escapes root")
	}
	return target, nil
}

func safeBundleName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if char == '-' || char == '_' || char == '.' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unnamed"
	}
	return builder.String()
}

func bundleFilePaths(files []BundleFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
