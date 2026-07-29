package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	datasetpkg "github.com/ziwu/aexp/internal/dataset"
	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	transferpkg "github.com/ziwu/aexp/internal/transfer"
)

func datasetRuntime(services *fileSpaceCLI) (*datasetpkg.Service, *transferpkg.Worker) {
	remote := filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: services.pool}}
	service := datasetpkg.NewService(services.db, services.planner, services.transfers, remote)
	transport := transferpkg.NewRsyncTransport(services.db, remote, transferpkg.SSHPoolTransferRunner{Pool: services.pool})
	return service, transferpkg.NewWorker(services.db, transport)
}

func datasetIngestCmd() *cobra.Command {
	var from, destination, format string
	var dryRun, asJSON bool
	cmd := &cobra.Command{
		Use:   "ingest <name@version>",
		Short: "Hash, atomically publish, verify, and immutably tag a dataset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			datasets, worker := datasetRuntime(services)
			planned, err := datasets.PlanIngest(cmd.Context(), args[0], from, destination)
			if err != nil {
				return err
			}
			if dryRun {
				return printJSON(planned)
			}
			job, created, err := datasets.StartIngest(cmd.Context(), args[0], from, destination, planned.Transfer.PlanSHA256)
			if err != nil {
				return err
			}
			if job.State != store.TransferCompleted {
				if err := worker.Execute(cmd.Context(), job.ID); err != nil {
					return fmt.Errorf("dataset publish transfer %s: %w", job.ID, err)
				}
			}
			dataset, registered, err := datasets.FinalizeIngest(cmd.Context(), args[0], job.ID, format)
			if err != nil {
				return err
			}
			result := map[string]any{"dataset": dataset, "transfer_id": job.ID, "transfer_created": created, "registry_created": registered}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s@%s\t%s\t%s\n", dataset.DatasetID, dataset.Version, dataset.Revision, job.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Local file or directory to publish (required)")
	cmd.Flags().StringVar(&destination, "to", "", "Destination aexp:// logical path (required)")
	cmd.Flags().StringVar(&format, "format", "directory", "Dataset format metadata")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Hash and show the side-effect-free transfer plan")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func datasetManagedMaterializeCmd() *cobra.Command {
	return datasetEnsureCmd("materialize <name@version>", "Ensure a verified dataset cache through a persistent TransferJob")
}

func datasetRepairCmd() *cobra.Command {
	return datasetEnsureCmd("repair <name@version>", "Repair a missing dataset cache through the shared TransferJob engine")
}

func datasetEnsureCmd(use, short string) *cobra.Command {
	var resourceName, targetPath string
	var dryRun, wait, asJSON bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			services, cleanup := openFileSpaceCLI()
			defer cleanup()
			resource, err := services.db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil {
				return err
			}
			if resource == nil {
				resource, err = services.db.GetResource(cmd.Context(), resourceName)
			}
			if err != nil || resource == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}
			datasets, worker := datasetRuntime(services)
			if dryRun {
				// Materialize itself performs a real destination stat. Dry-run uses
				// the generic side-effect-free planner once the dataset identity is
				// resolved by the regular command; no legacy rsync command is shown.
				datasetID, version, parseErr := parseDatasetRef(args[0])
				if parseErr != nil {
					return parseErr
				}
				dataset, getErr := services.db.GetDatasetVersionByRef(cmd.Context(), datasetID, version)
				if getErr != nil || dataset == nil {
					return fmt.Errorf("dataset %s not found", args[0])
				}
				return printJSON(map[string]any{"dataset": dataset, "resource": resource.Name, "target": targetPath, "operation": "managed ensure", "local_data_path": false})
			}
			result, err := datasets.Materialize(cmd.Context(), args[0], resource.ID, targetPath)
			if err != nil {
				return err
			}
			if wait && result.Transfer != nil && result.Transfer.State != store.TransferCompleted {
				if err := worker.Execute(cmd.Context(), result.Transfer.ID); err != nil {
					return err
				}
				ready, err := datasets.ReconcileMaterialization(cmd.Context(), result.Materialization.DatasetVersionID, resource.ID)
				if err != nil {
					return err
				}
				result.Materialization = *ready
			}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s\t%s\t%s\n", result.Materialization.ID, result.Materialization.State, result.Materialization.TransferID)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Destination compute resource name or id (required)")
	cmd.Flags().StringVar(&targetPath, "target", "", "Cache path relative to the resource root")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the managed ensure intent without writing state")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for verification and cache readiness")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func datasetVerifyCmd() *cobra.Command {
	var resourceName, targetPath string
	var asJSON bool
	cmd := &cobra.Command{Use: "verify <name@version>", Short: "Verify an existing compute cache without transferring data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		services, cleanup := openFileSpaceCLI()
		defer cleanup()
		resource, err := services.db.GetResourceByName(cmd.Context(), resourceName)
		if err != nil {
			return err
		}
		if resource == nil {
			resource, err = services.db.GetResource(cmd.Context(), resourceName)
		}
		if err != nil || resource == nil {
			return fmt.Errorf("resource %s not found", resourceName)
		}
		datasets, _ := datasetRuntime(services)
		materialization, err := datasets.Verify(cmd.Context(), args[0], resource.ID, targetPath)
		if asJSON {
			_ = printJSON(materialization)
		}
		if err != nil {
			return err
		}
		if !asJSON {
			fmt.Printf("%s\t%s\t%s\n", materialization.ID, materialization.State, materialization.VerifiedSHA256)
		}
		return nil
	}}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Compute resource name or id (required)")
	cmd.Flags().StringVar(&targetPath, "target", "", "Cache path relative to the resource root")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func datasetEvictCmd() *cobra.Command {
	var resourceName, expectedPlan string
	var dryRun, confirmed, asJSON bool
	cmd := &cobra.Command{Use: "evict <name@version>", Short: "Evict a dataset cache only when a matching authoritative copy is live", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		services, cleanup := openFileSpaceCLI()
		defer cleanup()
		datasetID, version, err := parseDatasetRef(args[0])
		if err != nil {
			return err
		}
		dataset, err := services.db.GetDatasetVersionByRef(cmd.Context(), datasetID, version)
		if err != nil || dataset == nil {
			return firstCLIError(err, fmt.Errorf("dataset %s not found", args[0]))
		}
		if dataset.LogicalURI == "" {
			return fmt.Errorf("legacy dataset %s has no logical URI and cannot use managed eviction", args[0])
		}
		resourceID, err := resolveOptionalResourceID(cmd.Context(), services.db, resourceName)
		if err != nil {
			return err
		}
		plan, err := services.files.PlanEviction(cmd.Context(), dataset.LogicalURI, resourceID)
		if err != nil {
			return err
		}
		if dryRun {
			if asJSON {
				return printJSON(plan)
			}
			fmt.Printf("plan: %s\npath: %s\nrevision: %s\nbytes: %d\n", plan.PlanSHA256, plan.SourcePhysicalPath, plan.SourceRevision, plan.Bytes)
			return nil
		}
		if !confirmed || expectedPlan == "" {
			return fmt.Errorf("dataset eviction requires --yes and --plan-sha256 from --dry-run")
		}
		executed, err := services.files.Evict(cmd.Context(), dataset.LogicalURI, resourceID, expectedPlan)
		if err != nil {
			return err
		}
		if materialization, getErr := services.db.GetDatasetMaterialization(cmd.Context(), dataset.ID, resourceID); getErr == nil && materialization != nil {
			now := time.Now().UTC()
			materialization.State, materialization.LastError, materialization.FinishedAt = store.MaterializationFailed, "cache evicted by confirmed managed operation", &now
			materialization.BytesPresent, materialization.VerifiedSHA256 = 0, ""
			if saveErr := services.db.SaveDatasetMaterialization(cmd.Context(), materialization); saveErr != nil {
				return saveErr
			}
		}
		if asJSON {
			return printJSON(map[string]any{"dataset": args[0], "status": "evicted", "plan": executed})
		}
		fmt.Printf("evicted %s cache from %s\n", args[0], resourceName)
		return nil
	}}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Cache resource name or id (required)")
	cmd.Flags().StringVar(&expectedPlan, "plan-sha256", "", "Expected eviction plan hash")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the exact cache eviction plan")
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm cache deletion")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func firstCLIError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func waitForManagedTransfer(ctx context.Context, db store.Store, id string, interval time.Duration) (*store.TransferJob, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		job, err := db.GetTransferJob(ctx, id)
		if err != nil {
			return nil, err
		}
		if job == nil {
			return nil, fmt.Errorf("transfer %s not found", id)
		}
		switch job.State {
		case store.TransferCompleted:
			return job, nil
		case store.TransferFailed, store.TransferBlocked, store.TransferCancelled:
			return job, fmt.Errorf("transfer %s ended in %s: %s", id, job.State, job.LastError)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
