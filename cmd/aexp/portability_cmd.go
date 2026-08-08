package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	portabilityservice "github.com/ziwu/aexp/internal/portability"
	"github.com/ziwu/aexp/internal/store"
)

func portabilityCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "portability", Short: "Audit, export, validate, and stage control-plane migration"}
	cmd.AddCommand(portabilityAuditCmd(), portabilityExportCmd(), portabilityImportCmd(), portabilityValidateCmd())
	return cmd
}

func portabilityAuditCmd() *cobra.Command {
	var dbPath, attachmentsRoot string
	var asJSON, strict bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect machine-bound paths, missing local files, and resource rebind requirements",
		Long: `Run an offline, read-only portability audit.

The audit opens an existing SQLite database without migrations, checks only
controller-local durable files, and never contacts SSH resources or storage.
It does not move data, rewrite paths, expose credentials, or resume Runs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedDB, resolvedAttachments := resolvePortabilitySource(dbPath, attachmentsRoot)
			db, err := store.OpenSQLiteReadOnly(resolvedDB)
			if err != nil {
				return err
			}
			defer db.Close()
			report, err := (portabilityservice.Service{Store: db, DatabasePath: resolvedDB, AttachmentsRoot: resolvedAttachments}).Audit(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				if err := writePortabilityJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else if err := portabilityservice.WriteHuman(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if strict && report.Summary.BlockingFindings > 0 {
				return fmt.Errorf("portability audit found %d blocking finding(s)", report.Summary.BlockingFindings)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "~/.aexp/aexp.db", "Existing aexp SQLite database path")
	cmd.Flags().StringVar(&attachmentsRoot, "attachments-root", "", "Managed attachment root (defaults beside the database)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the portability-audit-v1 JSON report")
	cmd.Flags().BoolVar(&strict, "strict", false, "Return a failure when blocking local-loss findings exist")
	return cmd
}

func portabilityExportCmd() *cobra.Command {
	var dbPath, attachmentsRoot, outputPath string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Create a verified control-plane bundle without copying remote datasets or artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedDB, resolvedAttachments := resolvePortabilitySource(dbPath, attachmentsRoot)
			resolvedOutput := strings.TrimSpace(outputPath)
			if resolvedOutput == "" {
				resolvedOutput = fmt.Sprintf("aexp-portability-%s.tar.gz", time.Now().UTC().Format("20060102T150405Z"))
			}
			resolvedOutput = expandPath(resolvedOutput)
			result, err := portabilityservice.Export(cmd.Context(), portabilityservice.ExportOptions{DatabasePath: resolvedDB, AttachmentsRoot: resolvedAttachments, OutputPath: resolvedOutput})
			if err != nil {
				return err
			}
			if asJSON {
				return writePortabilityJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Portability bundle created: %s\n", result.BundlePath)
			fmt.Fprintf(cmd.OutOrStdout(), "SHA-256: %s\n", result.BundleSHA256)
			fmt.Fprintf(cmd.OutOrStdout(), "Files: %d (database plus registered attachments)\n", len(result.Manifest.Files))
			fmt.Fprintln(cmd.OutOrStdout(), "Remote datasets, artifacts, project trees, and SSH credentials were not copied.")
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "~/.aexp/aexp.db", "Existing aexp SQLite database path")
	cmd.Flags().StringVar(&attachmentsRoot, "attachments-root", "", "Managed attachment root (defaults beside the database)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output .tar.gz path")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func portabilityImportCmd() *cobra.Command {
	var destination string
	var dryRun, asJSON bool
	var rawMappings []string
	cmd := &cobra.Command{
		Use:   "import <bundle.tar.gz>",
		Short: "Verify and stage a bundle in an isolated directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mappings, err := parsePortabilityMappings(rawMappings)
			if err != nil {
				return err
			}
			report, err := portabilityservice.Restore(cmd.Context(), portabilityservice.RestoreOptions{BundlePath: expandPath(args[0]), Destination: expandPath(destination), DryRun: dryRun, Mappings: mappings})
			if err != nil {
				return err
			}
			if asJSON {
				return writePortabilityJSON(cmd.OutOrStdout(), report)
			}
			return writePortabilityRestoreHuman(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVar(&destination, "to", "", "New isolated workspace directory (never replaces ~/.aexp)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Verify and simulate rewrites in a temporary directory")
	cmd.Flags().StringSliceVar(&rawMappings, "map-path", nil, "Explicit path prefix mapping OLD=NEW; repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func portabilityValidateCmd() *cobra.Command {
	var asJSON bool
	var rawMappings []string
	cmd := &cobra.Command{
		Use:   "validate <bundle.tar.gz>",
		Short: "Verify checksums, compatibility, mappings, attachments, and restored database readability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mappings, err := parsePortabilityMappings(rawMappings)
			if err != nil {
				return err
			}
			report, err := portabilityservice.Restore(cmd.Context(), portabilityservice.RestoreOptions{BundlePath: expandPath(args[0]), DryRun: true, Mappings: mappings})
			if err != nil {
				return err
			}
			if asJSON {
				return writePortabilityJSON(cmd.OutOrStdout(), report)
			}
			return writePortabilityRestoreHuman(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringSliceVar(&rawMappings, "map-path", nil, "Explicit path prefix mapping OLD=NEW; repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func resolvePortabilitySource(dbPath, attachmentsRoot string) (string, string) {
	resolvedDB := expandPath(dbPath)
	resolvedAttachments := strings.TrimSpace(attachmentsRoot)
	if resolvedAttachments == "" {
		resolvedAttachments = filepath.Join(filepath.Dir(resolvedDB), "attachments")
	} else {
		resolvedAttachments = expandPath(resolvedAttachments)
	}
	return resolvedDB, resolvedAttachments
}

func parsePortabilityMappings(raw []string) ([]store.PathPrefixMapping, error) {
	mappings := make([]store.PathPrefixMapping, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		from, to, ok := strings.Cut(strings.TrimSpace(item), "=")
		from = filepath.Clean(expandPath(strings.TrimSpace(from)))
		to = filepath.Clean(expandPath(strings.TrimSpace(to)))
		if !ok || !filepath.IsAbs(from) || !filepath.IsAbs(to) {
			return nil, fmt.Errorf("invalid --map-path %q: expected absolute OLD=NEW", item)
		}
		if from == to {
			return nil, fmt.Errorf("invalid --map-path %q: OLD and NEW are identical", item)
		}
		if _, exists := seen[from]; exists {
			return nil, fmt.Errorf("duplicate --map-path source %s", from)
		}
		seen[from] = struct{}{}
		mappings = append(mappings, store.PathPrefixMapping{From: from, To: to})
	}
	sort.Slice(mappings, func(i, j int) bool { return len(mappings[i].From) > len(mappings[j].From) })
	return mappings, nil
}

func writePortabilityJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writePortabilityRestoreHuman(w io.Writer, report portabilityservice.RestoreReport) error {
	if _, err := fmt.Fprintf(w, "Portability restore validation: %s\n", report.Status); err != nil {
		return err
	}
	fmt.Fprintf(w, "dry run:          %t\n", report.DryRun)
	if report.Destination != "" {
		fmt.Fprintf(w, "destination:      %s\n", report.Destination)
	}
	fmt.Fprintf(w, "files verified:   %d\n", report.FilesVerified)
	fmt.Fprintf(w, "database:         integrity %s\n", report.DBIntegrity)
	fmt.Fprintf(w, "paths remapped:   %d\n", report.Rewrite.MappedPathsUpdated)
	fmt.Fprintf(w, "attachments:      %d restored references\n", report.Rewrite.AttachmentPathsUpdated)
	fmt.Fprintf(w, "resource rebinds: %d credentials cleared\n", report.Rewrite.ResourceBindingsCleared)
	fmt.Fprintf(w, "audit findings:   %d blocking / %d warnings\n", report.Audit.Summary.BlockingFindings, report.Audit.Summary.Warnings)
	fmt.Fprintln(w, "No service was started and no Run was resumed.")
	return nil
}
