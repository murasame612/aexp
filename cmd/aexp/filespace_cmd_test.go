package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ziwu/aexp/internal/store"
)

func TestFileSpaceAndTransferCommandSurface(t *testing.T) {
	for _, test := range []struct {
		root  commandFactory
		path  []string
		flags []string
	}{
		{root: fsCmd, path: []string{"roots"}, flags: []string{"json", "workspace"}},
		{root: fsCmd, path: []string{"root", "add"}, flags: []string{"workspace", "storage", "path"}},
		{root: fsCmd, path: []string{"stat"}, flags: []string{"json", "on"}},
		{root: fsCmd, path: []string{"ls"}, flags: []string{"json", "on", "limit", "cursor"}},
		{root: fsCmd, path: []string{"ensure"}, flags: []string{"json", "source-revision", "initiator", "verify", "plan-sha256", "wait"}},
		{root: fsCmd, path: []string{"evict"}, flags: []string{"json", "from", "dry-run", "plan-sha256", "yes"}},
		{root: transferCmd, path: []string{"plan"}, flags: []string{"json", "source-revision", "initiator", "verify"}},
		{root: transferCmd, path: []string{"start"}, flags: []string{"json", "plan-sha256"}},
		{root: transferCmd, path: []string{"status"}, flags: []string{"json"}},
		{root: storageCmd, path: []string{"stat"}, flags: []string{"json", "on"}},
		{root: storageCmd, path: []string{"ls"}, flags: []string{"json", "on", "limit", "cursor"}},
		{root: storageCmd, path: []string{"locations"}, flags: []string{"json"}},
		{root: storageCmd, path: []string{"copy"}, flags: []string{"json"}},
	} {
		cmd, _, err := test.root().Find(test.path)
		if err != nil {
			t.Fatalf("find %v: %v", test.path, err)
		}
		for _, name := range test.flags {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("%v missing --%s", test.path, name)
			}
		}
	}
}

type commandFactory func() *cobra.Command

func TestResolveOptionalResourceIDAcceptsNameAndID(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{ID: "resource_id", Name: "resource-name", Type: store.ResourceTypeSSH, Host: "host", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"resource_id", "resource-name"} {
		id, err := resolveOptionalResourceID(ctx, db, value)
		if err != nil || id != "resource_id" {
			t.Fatalf("resolve %q id=%q err=%v", value, id, err)
		}
	}
	if _, err := resolveOptionalResourceID(ctx, db, "missing"); err == nil {
		t.Fatal("missing resource was accepted")
	}
}
