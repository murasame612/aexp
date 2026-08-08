package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziwu/aexp/internal/portability"
	"github.com/ziwu/aexp/internal/store"
)

func TestPortabilityAuditCommandJSONAndStrictExit(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "aexp.db")
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectDefinition(context.Background(), &store.ProjectDefinition{ID: "project_missing", Name: "Missing", LocalRoot: filepath.Join(t.TempDir(), "not-restored")}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := portabilityAuditCmd()
	var output bytes.Buffer
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--db", dbPath, "--json", "--strict"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "blocking finding") {
		t.Fatalf("strict audit error = %v", err)
	}
	var report portability.Report
	if decodeErr := json.Unmarshal(output.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode JSON output: %v\n%s", decodeErr, output.String())
	}
	if report.SchemaVersion != portability.SchemaVersion || report.Mode != portability.ModeReadOnly || report.Summary.BlockingFindings != 1 {
		t.Fatalf("unexpected CLI report: %#v", report)
	}
}

func TestPortabilityCommandSurfaceAndMappingValidation(t *testing.T) {
	t.Parallel()
	root := portabilityCmd()
	for _, name := range []string{"audit", "export", "import", "validate"} {
		child, _, err := root.Find([]string{name})
		if err != nil || child == nil || child.Name() != name {
			t.Fatalf("portability command %q missing: child=%v err=%v", name, child, err)
		}
	}
	mappings, err := parsePortabilityMappings([]string{"/old=/new", "/old/project=/new/project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 2 || mappings[0].From != "/old/project" {
		t.Fatalf("mappings were not ordered most-specific first: %#v", mappings)
	}
	if _, err := parsePortabilityMappings([]string{"relative=/new"}); err == nil {
		t.Fatal("relative mapping source must be rejected")
	}
}

func TestPortabilityExportAndValidateCommands(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "aexp.db")
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle.tar.gz")

	exportCmd := portabilityExportCmd()
	var exportOutput bytes.Buffer
	exportCmd.SetOut(&exportOutput)
	exportCmd.SetArgs([]string{"--db", dbPath, "--attachments-root", filepath.Join(root, "attachments"), "--output", bundlePath, "--json"})
	if err := exportCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var exported portability.ExportResult
	if err := json.Unmarshal(exportOutput.Bytes(), &exported); err != nil {
		t.Fatalf("decode export: %v\n%s", err, exportOutput.String())
	}
	if exported.BundlePath != bundlePath || len(exported.Manifest.Files) != 1 {
		t.Fatalf("exported = %#v", exported)
	}

	validateCmd := portabilityValidateCmd()
	var validateOutput bytes.Buffer
	validateCmd.SetOut(&validateOutput)
	validateCmd.SetArgs([]string{bundlePath, "--json"})
	if err := validateCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report portability.RestoreReport
	if err := json.Unmarshal(validateOutput.Bytes(), &report); err != nil {
		t.Fatalf("decode validate: %v\n%s", err, validateOutput.String())
	}
	if !report.DryRun || report.Status != "valid" || report.DBIntegrity != "ok" {
		t.Fatalf("validate report = %#v", report)
	}
}
