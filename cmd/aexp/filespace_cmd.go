package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	transferpkg "github.com/ziwu/aexp/internal/transfer"
)

type fileSpaceCLI struct {
	db        *store.SQLite
	pool      *executor.SSHPool
	files     *filespace.Service
	planner   *transferpkg.Planner
	transfers *transferpkg.Service
}

func openFileSpaceCLI() (*fileSpaceCLI, func()) {
	db := openDB()
	pool := executor.NewSSHPool(10 * time.Second)
	loadSSHKeys(pool)
	files := filespace.NewService(db, filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: pool}})
	planner := transferpkg.NewPlanner(db, files)
	services := &fileSpaceCLI{db: db, pool: pool, files: files, planner: planner, transfers: transferpkg.NewService(db, planner)}
	return services, func() {
		pool.CloseAll()
		db.Close()
	}
}

func fsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "fs", Short: "Inspect aexp logical paths and their physical placements"}
	cmd.AddCommand(fsRootsCmd(), fsRootCmd(), fsResolveCmd(), fsLocateCmd(), fsStatCmd(), fsListCmd(), fsHashCmd(), fsEnsureCmd(), fsEvictCmd())
	return cmd
}

func storageStatCmd() *cobra.Command {
	var resourceName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stat <uri>",
		Short: "Inspect an aexp://, storage://, or resource:// path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
			if err != nil {
				return err
			}
			result, err := services.files.StatURI(cmd.Context(), args[0], resourceID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			location := result.Location
			fmt.Printf("%s\t%s\t%s\t%d\t%s\n", location.State, location.Role, location.ResourceName, location.Bytes, location.URI)
			if location.Error != "" {
				fmt.Fprintf(os.Stderr, "observation error: %s\n", location.Error)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "on", "", "Optional logical placement resource name or id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func storageLsCmd() *cobra.Command {
	var resourceName, cursor string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls <uri>",
		Short: "List one bounded page from an aexp://, storage://, or resource:// directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
			if err != nil {
				return err
			}
			result, err := services.files.ListURI(cmd.Context(), args[0], resourceID, cursor, limit)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			for _, entry := range result.Entries {
				fmt.Printf("%s\t%d\t%s\n", entry.Type, entry.Size, entry.Name)
			}
			if result.NextCursor != "" {
				fmt.Fprintf(os.Stderr, "next cursor: %s\n", result.NextCursor)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "on", "", "Optional logical placement resource name or id")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue after this entry name")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum entries (1-500)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func storageLocationsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "locations <uri>",
		Short: "Show where a path is stored or cached",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			locations, err := services.files.LocationsURI(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"uri": args[0], "locations": locations, "total": len(locations)})
			}
			for _, location := range locations {
				fmt.Printf("%s\t%s\t%s\t%s\n", location.Role, location.State, location.ResourceName, location.URI)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func storageCopyCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "copy <source-uri> <destination-uri>",
		Short: "Discover the source revision and queue a verified no-overwrite copy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			request := transferpkg.PlanRequest{Source: args[0], Destination: args[1], Initiator: "auto", Verification: "sha256"}
			job, created, plan, err := services.transfers.CreateCurrent(cmd.Context(), request)
			if err != nil {
				var blocked *transferpkg.PlanBlockedError
				if asJSON && errors.As(err, &blocked) {
					return printJSON(map[string]any{"accepted": false, "state": "blocked", "source": args[0], "destination": args[1], "blockers": blocked.Blockers})
				}
				return err
			}
			result := map[string]any{"accepted": true, "transfer_id": job.ID, "state": job.State, "created": created, "source": plan.Source.URI, "destination": plan.Destination.URI, "source_revision": plan.Source.Revision, "total_bytes": plan.TotalBytes, "file_count": plan.FileCount}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s\t%s\t%s -> %s\n", job.ID, job.State, plan.Source.URI, plan.Destination.URI)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output compact JSON")
	return cmd
}

func fsEvictCmd() *cobra.Command {
	var resourceName, expectedPlan string
	var dryRun, confirmed, asJSON bool
	cmd := &cobra.Command{
		Use:   "evict <aexp-uri>",
		Short: "Safely evict one placement after verifying another authoritative copy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
			if err != nil {
				return err
			}
			if resourceID == "" {
				return fmt.Errorf("--from resource is required")
			}
			plan, err := services.files.PlanEviction(cmd.Context(), args[0], resourceID)
			if err != nil {
				return err
			}
			if dryRun {
				if asJSON {
					return printJSON(plan)
				}
				fmt.Printf("plan: %s\npath: %s\nrevision: %s\nbytes: %d\nkeeper: %s\n", plan.PlanSHA256, plan.SourcePhysicalPath, plan.SourceRevision, plan.Bytes, plan.AuthoritativePlacementID)
				return nil
			}
			if !confirmed {
				return fmt.Errorf("eviction requires --yes after reviewing --dry-run")
			}
			if expectedPlan == "" {
				return fmt.Errorf("eviction requires --plan-sha256 from --dry-run")
			}
			executed, err := services.files.Evict(cmd.Context(), args[0], resourceID, expectedPlan)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"status": "evicted", "plan": executed})
			}
			fmt.Printf("evicted %s from %s (%s)\n", executed.LogicalURI, executed.SourceResourceID, executed.SourcePhysicalPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "from", "", "Placement resource name or id (required)")
	cmd.Flags().StringVar(&expectedPlan, "plan-sha256", "", "Expected eviction plan hash")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show exact path, revision, bytes, and authoritative keeper")
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm deletion of the planned placement")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

func fsEnsureCmd() *cobra.Command {
	var flags transferFlags
	var expectedPlan string
	var wait, asJSON bool
	cmd := &cobra.Command{
		Use:   "ensure <source-uri> <destination-uri>",
		Short: "Ensure a verified placement through the shared TransferJob engine",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			request := flags.request(args)
			if expectedPlan == "" {
				planned, err := services.planner.Build(cmd.Context(), request)
				if err != nil {
					return err
				}
				expectedPlan = planned.PlanSHA256
			}
			job, created, err := services.transfers.Create(cmd.Context(), request, expectedPlan)
			if err != nil {
				return err
			}
			if wait && job.State != store.TransferCompleted {
				remote := filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: services.pool}}
				transport := transferpkg.NewRsyncTransport(services.db, remote, transferpkg.SSHPoolTransferRunner{Pool: services.pool})
				worker := transferpkg.NewWorker(services.db, transport)
				if err := worker.Execute(cmd.Context(), job.ID); err != nil {
					return err
				}
				job, err = services.db.GetTransferJob(cmd.Context(), job.ID)
				if err != nil {
					return err
				}
			}
			if asJSON {
				return printJSON(map[string]any{"transfer": job, "created": created})
			}
			fmt.Printf("%s\t%s\n", job.ID, job.State)
			return nil
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&expectedPlan, "plan-sha256", "", "Expected plan hash; omitted only for an atomic CLI plan+start")
	cmd.Flags().BoolVar(&wait, "wait", false, "Execute and wait for verification in this process")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func fsRootsCmd() *cobra.Command {
	var workspace string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "roots",
		Short: "List logical roots",
		RunE: func(cmd *cobra.Command, _ []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			roots, err := services.db.ListLogicalRoots(cmd.Context(), workspace)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"items": roots, "total": len(roots)})
			}
			for _, root := range roots {
				fmt.Printf("%s\taexp://%s/%s\t%s:%s\n", root.ID, root.Workspace, root.Prefix, root.StorageTargetID, root.PhysicalRoot)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Filter by workspace")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func fsRootCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "root", Short: "Create or remove a logical root"}
	cmd.AddCommand(fsRootAddCmd(), fsRootRemoveCmd())
	return cmd
}

func fsRootAddCmd() *cobra.Command {
	var workspace, storageName, physicalRoot, id string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add <prefix>",
		Short: "Register a logical root on an existing storage target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			target, err := services.db.GetStorageTargetByName(cmd.Context(), storageName)
			if err != nil {
				return err
			}
			if target == nil {
				return fmt.Errorf("storage target %s not found", storageName)
			}
			if id == "" {
				id = genID("root_")
			}
			root := &store.LogicalRoot{ID: id, Workspace: workspace, Prefix: args[0], StorageTargetID: target.ID, PhysicalRoot: physicalRoot}
			if err := services.db.SaveLogicalRoot(cmd.Context(), root); err != nil {
				return err
			}
			if asJSON {
				return printJSON(root)
			}
			fmt.Printf("registered %s as aexp://%s/%s\n", root.ID, root.Workspace, root.Prefix)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Logical workspace (required)")
	cmd.Flags().StringVar(&storageName, "storage", "", "Storage target name (required)")
	cmd.Flags().StringVar(&physicalRoot, "path", "", "Path relative to the storage target root (required)")
	cmd.Flags().StringVar(&id, "id", "", "Explicit logical root id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("storage")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func fsRootRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <root-id>",
		Short: "Remove root metadata without deleting any files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			if err := services.db.DeleteLogicalRoot(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Println("logical root metadata removed; no payload was deleted")
			return nil
		},
	}
}

func fsResolveCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "resolve <aexp-uri>",
		Short: "Resolve a logical path without touching the remote filesystem",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			result, err := services.files.Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s -> %s:%s\n", result.LogicalURI, result.DefaultPlacement.ResourceID, result.DefaultPlacement.PhysicalPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func fsLocateCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "locate <aexp-uri>",
		Short: "List registered placements without claiming they currently exist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			items, err := services.files.Locate(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"items": items, "total": len(items)})
			}
			for _, item := range items {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", item.ResourceID, item.Role, item.ObservedState, item.Freshness, item.PhysicalPath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func fsStatCmd() *cobra.Command {
	var resourceName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stat <aexp-uri>",
		Short: "Refresh and persist a placement observation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
			if err != nil {
				return err
			}
			result, err := services.files.Inspect(cmd.Context(), args[0], resourceID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			p := result.Placement
			fmt.Printf("%s\t%s\t%s\t%s\n", p.ObservedState, p.Freshness, p.ResourceID, p.PhysicalPath)
			if p.ObservationError != "" {
				fmt.Fprintf(os.Stderr, "observation error: %s\n", p.ObservationError)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "on", "", "Inspect the placement on this resource name or id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func fsListCmd() *cobra.Command {
	var resourceName, cursor string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls <aexp-uri>",
		Short: "List one remote directory page",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
			if err != nil {
				return err
			}
			result, err := services.files.List(cmd.Context(), args[0], resourceID, cursor, limit)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			for _, entry := range result.Entries {
				fmt.Printf("%s\t%d\t%s\n", entry.Type, entry.Size, entry.Name)
			}
			if result.NextCursor != "" {
				fmt.Fprintf(os.Stderr, "next cursor: %s\n", result.NextCursor)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "on", "", "List the placement on this resource name or id")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue after this entry name")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum entries (1-500)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func fsHashCmd() *cobra.Command {
	var resourceName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "hash <aexp-uri>",
		Short: "Compute a complete remote SHA-256 revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
			if err != nil {
				return err
			}
			result, err := services.files.Hash(cmd.Context(), args[0], resourceID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s\t%d files\t%d bytes\n", result.Revision, result.FileCount, result.TotalBytes)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "on", "", "Hash the placement on this resource name or id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func transferCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "transfer", Short: "Plan and track managed cross-resource transfers"}
	cmd.AddCommand(transferPlanCmd(), transferStartCmd(), transferStatusCmd(), transferListCmd(), transferRetryCmd(), transferCancelCmd())
	return cmd
}

type transferFlags struct {
	sourceRevision string
	initiator      string
	verification   string
}

func (f *transferFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.sourceRevision, "source-revision", "", "Pinned source SHA-256 revision")
	cmd.Flags().StringVar(&f.initiator, "initiator", "auto", "auto, nas, compute, or mac")
	cmd.Flags().StringVar(&f.verification, "verify", "sha256", "sha256, manifest, or none")
}

func (f transferFlags) request(args []string) transferpkg.PlanRequest {
	return transferpkg.PlanRequest{Source: args[0], Destination: args[1], SourceRevision: f.sourceRevision, Initiator: f.initiator, Verification: f.verification}
}

func transferPlanCmd() *cobra.Command {
	var flags transferFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "plan <source-uri> <destination-uri>",
		Short: "Build a side-effect-free transfer plan",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			plan, err := services.planner.Build(cmd.Context(), flags.request(args))
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(plan)
			}
			fmt.Printf("plan: %s\nroute: %s (%s)\npayload via Mac: %v\n", plan.PlanSHA256, plan.Initiator, plan.CommandResourceID, plan.LocalDataPath)
			for _, blocker := range plan.Blockers {
				fmt.Printf("blocker: %s: %s\n", blocker.Code, blocker.Message)
			}
			return nil
		},
	}
	flags.add(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func transferStartCmd() *cobra.Command {
	var flags transferFlags
	var expectedPlan string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "start <source-uri> <destination-uri>",
		Short: "Recompute an accepted plan and persist an asynchronous transfer job",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			job, created, err := services.transfers.Create(cmd.Context(), flags.request(args), expectedPlan)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"transfer": job, "created": created})
			}
			fmt.Printf("%s\t%s\n", job.ID, job.State)
			return nil
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&expectedPlan, "plan-sha256", "", "Expected hash returned by transfer plan (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("plan-sha256")
	return cmd
}

func transferStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <transfer-id>",
		Short: "Show a transfer job and attempt ledger",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			detail, err := services.transfers.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if detail == nil {
				return fmt.Errorf("transfer %s not found", args[0])
			}
			if asJSON {
				return printJSON(detail)
			}
			fmt.Printf("%s\t%s\t%s\t%d/%d bytes\n", detail.Job.ID, detail.Job.State, detail.Job.Stage, detail.Job.BytesDone, detail.Job.TotalBytes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func transferListCmd() *cobra.Command {
	var state, workspace string
	var limit, offset int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List compact transfer summaries",
		RunE: func(cmd *cobra.Command, _ []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			jobs, err := services.db.ListTransferJobsPage(cmd.Context(), state, workspace, nil, limit, offset)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"items": jobs, "total": len(jobs)})
			}
			for _, job := range jobs {
				fmt.Printf("%s\t%s\t%s\t%d/%d\n", job.ID, job.State, job.Stage, job.BytesDone, job.TotalBytes)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "Filter by state")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Filter by logical workspace")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum jobs")
	cmd.Flags().IntVar(&offset, "offset", 0, "Pagination cursor offset")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func transferRetryCmd() *cobra.Command {
	return transferMutationCmd("retry", func(ctx context.Context, service *transferpkg.Service, id string) (*store.TransferJob, error) {
		return service.Retry(ctx, id)
	})
}

func transferCancelCmd() *cobra.Command {
	return transferMutationCmd("cancel", func(ctx context.Context, service *transferpkg.Service, id string) (*store.TransferJob, error) {
		return service.Cancel(ctx, id)
	})
}

func transferMutationCmd(action string, mutate func(context.Context, *transferpkg.Service, string) (*store.TransferJob, error)) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   action + " <transfer-id>",
		Short: action + " a managed transfer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			job, err := mutate(cmd.Context(), services.transfers, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(job)
			}
			fmt.Printf("%s\t%s\n", job.ID, job.State)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func resolveOptionalResourceID(ctx context.Context, db store.Store, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", nil
	}
	resource, err := db.GetResource(ctx, nameOrID)
	if err != nil {
		return "", err
	}
	if resource == nil {
		resource, err = db.GetResourceByName(ctx, nameOrID)
		if err != nil {
			return "", err
		}
	}
	if resource == nil {
		return "", fmt.Errorf("resource %s not found", nameOrID)
	}
	return resource.ID, nil
}
