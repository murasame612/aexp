package store

import (
	"path/filepath"
	"testing"
)

func TestPrinterEnabledEpochDoesNotAdvanceWhenDatabaseReopens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "aexp.db")
	db, err := NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigurePrinter(t.Context(), true, "Printer_POS_80"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.CreateResource(t.Context(), &Resource{ID: "printer_resource", Name: "printer-resource", Host: "localhost", User: "u", RootDir: "/tmp"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.CreateRun(t.Context(), &Run{ID: "printer_run", ResourceID: "printer_resource", Status: RunStatusQueued, Kind: RunKindSmoke, Command: "true"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	before, err := db.GetPrinterSettings(t.Context())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.GetPrinterSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.EnabledFromEventSeq != after.EnabledFromEventSeq {
		t.Fatalf("enabled epoch advanced across reopen: before=%d after=%d", before.EnabledFromEventSeq, after.EnabledFromEventSeq)
	}
	if after.EnabledFromEventSeq != 0 {
		t.Fatalf("enabled epoch = %d, want 0", after.EnabledFromEventSeq)
	}
}
