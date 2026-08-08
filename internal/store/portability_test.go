package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPreparePortabilityCopyClearsBindingsAndMapsKnownPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestStore(t)
	oldRoot := filepath.Join(string(filepath.Separator), "Users", "old", "research")
	newRoot := filepath.Join(string(filepath.Separator), "home", "new", "research")
	resource := &Resource{ID: "portable_resource", Name: "portable", Type: ResourceTypeSSH, Host: "example", AuthRef: "/secret/key", SocksProxy: "secret", ProxyCommand: "secret", RootDir: filepath.Join(oldRoot, "remote"), SSHStatus: "ok"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	project := &ProjectDefinition{ID: "portable_project", Name: "Portable", LocalRoot: filepath.Join(oldRoot, "project")}
	if err := db.SaveProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &Run{ID: "portable_run", ResourceID: resource.ID, ProjectID: project.ID, Status: RunStatusSucceeded, Command: "true", Cwd: filepath.Join(oldRoot, "project"), ResolvedCwd: filepath.Join(oldRoot, "project")}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(ctx, &RunMark{ID: "portable_mark", RunID: "portable_run", Actor: "agent", Kind: "key_result"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMarkAttachments(ctx, "portable_mark", []RunMarkAttachment{{ID: "portable_attachment", MarkID: "portable_mark", Filename: "plot.png", LocalPath: filepath.Join(oldRoot, "attachments", "plot.png")}}); err != nil {
		t.Fatal(err)
	}

	restoredAttachment := filepath.Join(newRoot, "bundle", "attachments", "plot.png")
	summary, err := db.PreparePortabilityCopy(ctx, map[string]string{"portable_attachment": restoredAttachment}, []PathPrefixMapping{{From: oldRoot, To: newRoot}})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ResourceBindingsCleared != 1 || summary.AttachmentPathsUpdated != 1 || summary.MappedPathsUpdated < 3 {
		t.Fatalf("rewrite summary = %#v", summary)
	}
	gotResource, err := db.GetResource(ctx, resource.ID)
	if err != nil || gotResource == nil {
		t.Fatal(err)
	}
	if gotResource.AuthRef != "" || gotResource.SocksProxy != "" || gotResource.ProxyCommand != "" || gotResource.SSHStatus != "unknown" || gotResource.RootDir != filepath.Join(newRoot, "remote") {
		t.Fatalf("resource after import = %#v", gotResource)
	}
	gotProject, err := db.GetProjectDefinition(ctx, project.ID)
	if err != nil || gotProject == nil || gotProject.LocalRoot != filepath.Join(newRoot, "project") {
		t.Fatalf("project after import = %#v err=%v", gotProject, err)
	}
	mark, err := db.GetRunMark(ctx, "portable_mark")
	if err != nil || mark == nil || len(mark.Attachments) != 1 || mark.Attachments[0].LocalPath != restoredAttachment {
		t.Fatalf("attachment after import = %#v err=%v", mark, err)
	}
}
