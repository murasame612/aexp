package portability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestExportValidateAndImportPortableControlPlane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	oldRoot := filepath.Join(root, "old-controller", "research")
	newRoot := filepath.Join(root, "new-controller", "research")
	attachmentsRoot := filepath.Join(root, "source", "attachments")
	dbPath := filepath.Join(root, "source", "aexp.db")
	for _, dir := range []string{filepath.Join(oldRoot, "project"), filepath.Join(newRoot, "project"), attachmentsRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	attachmentPath := filepath.Join(attachmentsRoot, "plot.png")
	if err := os.WriteFile(attachmentPath, []byte("portable plot"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	secret := "SHOULD_NOT_SURVIVE_RESOURCE_REBIND"
	resource := &store.Resource{ID: "bundle_resource", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu.example", RootDir: filepath.Join(oldRoot, "remote"), AuthRef: secret, SocksProxy: secret, ProxyCommand: secret, SSHStatus: "ok"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	project := &store.ProjectDefinition{ID: "bundle_project", Name: "Bundle", LocalRoot: filepath.Join(oldRoot, "project")}
	if err := db.SaveProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "bundle_run", ResourceID: resource.ID, ProjectID: project.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, Command: "true", Cwd: filepath.Join(oldRoot, "project")}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(ctx, &store.RunMark{ID: "bundle_mark", RunID: "bundle_run", Actor: "agent", Kind: "key_result"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMarkAttachments(ctx, "bundle_mark", []store.RunMarkAttachment{{ID: "bundle_attachment", MarkID: "bundle_mark", Filename: "plot.png", LocalPath: attachmentPath}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(root, "exports", "control-plane.tar.gz")
	fixedNow := time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)
	exported, err := Export(ctx, ExportOptions{DatabasePath: dbPath, AttachmentsRoot: attachmentsRoot, OutputPath: bundlePath, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.SchemaVersion != BundleSchemaVersion || exported.Manifest.CreatedAt != fixedNow || len(exported.Manifest.Files) != 2 || !strings.HasPrefix(exported.BundleSHA256, "sha256:") {
		t.Fatalf("export result = %#v", exported)
	}

	mappings := []store.PathPrefixMapping{{From: oldRoot, To: newRoot}}
	validated, err := Restore(ctx, RestoreOptions{BundlePath: bundlePath, DryRun: true, Mappings: mappings})
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != "valid_with_findings" || !validated.DryRun || validated.FilesVerified != 2 || validated.DBIntegrity != "ok" || validated.Rewrite.ResourceBindingsCleared != 1 || validated.Rewrite.AttachmentPathsUpdated != 1 {
		t.Fatalf("validation report = %#v", validated)
	}

	destination := filepath.Join(root, "restored")
	restored, err := Restore(ctx, RestoreOptions{BundlePath: bundlePath, Destination: destination, Mappings: mappings})
	if err != nil {
		t.Fatal(err)
	}
	if restored.DryRun || restored.Destination != destination {
		t.Fatalf("restore report = %#v", restored)
	}
	restoredDB, err := store.OpenSQLiteReadOnly(filepath.Join(destination, "database", "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	gotResource, err := restoredDB.GetResource(ctx, resource.ID)
	if err != nil || gotResource == nil || gotResource.AuthRef != "" || gotResource.SocksProxy != "" || gotResource.ProxyCommand != "" || gotResource.RootDir != filepath.Join(newRoot, "remote") {
		t.Fatalf("restored resource = %#v err=%v", gotResource, err)
	}
	gotProject, err := restoredDB.GetProjectDefinition(ctx, project.ID)
	if err != nil || gotProject == nil || gotProject.LocalRoot != filepath.Join(newRoot, "project") {
		t.Fatalf("restored project = %#v err=%v", gotProject, err)
	}
	mark, err := restoredDB.GetRunMark(ctx, "bundle_mark")
	if err != nil || mark == nil || len(mark.Attachments) != 1 {
		t.Fatalf("restored mark = %#v err=%v", mark, err)
	}
	if !strings.HasPrefix(mark.Attachments[0].LocalPath, filepath.Join(destination, "attachments")) {
		t.Fatalf("restored attachment path = %s", mark.Attachments[0].LocalPath)
	}
	if data, err := os.ReadFile(mark.Attachments[0].LocalPath); err != nil || string(data) != "portable plot" {
		t.Fatalf("restored attachment data = %q err=%v", data, err)
	}
}

func TestExportRefusesMissingControllerFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "aexp.db")
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectDefinition(ctx, &store.ProjectDefinition{ID: "missing_project", Name: "Missing", LocalRoot: filepath.Join(root, "missing")}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Export(ctx, ExportOptions{DatabasePath: dbPath, AttachmentsRoot: filepath.Join(root, "attachments"), OutputPath: filepath.Join(root, "blocked.tar.gz")})
	if err == nil || !strings.Contains(err.Error(), "export blocked") {
		t.Fatalf("export error = %v", err)
	}
}

func TestRestoreRejectsUnexpectedBundleMember(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bundleRoot := filepath.Join(root, "contents")
	if err := os.MkdirAll(bundleRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(bundleRoot, "database", "aexp.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	databaseFile, err := describeBundleFile(databasePath, "database/aexp.db", "database", "", "source")
	if err != nil {
		t.Fatal(err)
	}
	manifest := BundleManifest{SchemaVersion: BundleSchemaVersion, CreatedAt: time.Now().UTC(), DatabasePath: "database/aexp.db", Files: []BundleFile{databaseFile}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleRoot, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleRoot, "unexpected.txt"), []byte("not declared"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "unexpected.tar.gz")
	if err := writeTarGz(bundlePath, bundleRoot, []string{"manifest.json", "database/aexp.db", "unexpected.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), RestoreOptions{BundlePath: bundlePath, DryRun: true}); err == nil || !strings.Contains(err.Error(), "unexpected bundle member") {
		t.Fatalf("restore error = %v", err)
	}
}
