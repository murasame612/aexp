package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/spf13/cobra"

	"github.com/ziwu/aexp/internal/api"
	datasetservice "github.com/ziwu/aexp/internal/dataset"
	"github.com/ziwu/aexp/internal/eventcache"
	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/explore"
	"github.com/ziwu/aexp/internal/filespace"
	freezer "github.com/ziwu/aexp/internal/freeze"
	"github.com/ziwu/aexp/internal/mcp"
	"github.com/ziwu/aexp/internal/monitor"
	printerservice "github.com/ziwu/aexp/internal/printer"
	releaseservice "github.com/ziwu/aexp/internal/release"
	runioservice "github.com/ziwu/aexp/internal/runio"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

var (
	version = "dev"
	logger  *slog.Logger
)

func main() {
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	root := &cobra.Command{
		Use:   "aexp",
		Short: "Agent Experiment Control Plane",
		Long: `aexp — 面向人-Agent 协作的科研实验运行中间层

aexp runs locally and dispatches commands to registered resources over SSH.
It does not need to be installed on the remote host.

Primary workflow:
  Project → Asset revision → Run → Project journal → Snapshot/Release → Evidence proposal

  - Publish immutable inputs with "aexp asset publish".
  - Submit and inspect experiments with "aexp run".
  - Preserve daily research reasoning with "aexp project journal".
  - Reference verified outputs with "aexp snapshot create".
  - Evaluate the Project gate with "aexp release evaluate".
  - Promote durable claims with "aexp evidence proposal".

Transport, placement, storage, and binding commands remain available as
deprecated administrator compatibility tools, but are not part of the normal
research workflow.`,
		Version:      version,
		SilenceUsage: true,
	}

	root.AddCommand(
		agentCmd(),
		assetCmd(),
		doctorCmd(),
		evidenceCmd(),
		eventCmd(),
		matrixCmd(),
		mcpCmd(),
		projectCmd(),
		printerCmd(),
		storageCmd(),
		fsCmd(),
		datasetCmd(),
		snapshotCmd(),
		releaseCmd(),
		freezeCmd(),
		transferCmd(),
		syncCmd(),
		updateCmd(),
		serveCmd(),
		uninstallCmd(),
		initCmd(),
		resourceCmd(),
		runCmd(),
		execCmd(),
	)
	hideCompatibilityCommands(root,
		"dataset", "exec", "freeze", "fs", "matrix", "printer",
		"resource", "storage", "sync", "transfer",
	)

	if filepath.Base(os.Args[0]) == "aexp-event" {
		root.SetArgs(append([]string{"event"}, os.Args[1:]...))
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func hideCompatibilityCommands(root *cobra.Command, names ...string) {
	hidden := make(map[string]struct{}, len(names))
	for _, name := range names {
		hidden[name] = struct{}{}
	}
	for _, command := range root.Commands() {
		if _, ok := hidden[command.Name()]; ok {
			command.Hidden = true
		}
	}
}

// --- data center ---

func storageCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "storage", Short: "Manage remote storage targets and Agent-visible files"}
	cmd.AddCommand(storageAddCmd(), storageListCmd(), storageDoctorCmd(), storageStatCmd(), storageLsCmd(), storageLocationsCmd(), storageCopyCmd())
	return cmd
}

func storageAddCmd() *cobra.Command {
	var resourceName, rootPath string
	cmd := &cobra.Command{
		Use: "add <name>", Short: "Register a NAS or remote store without moving data through this Mac", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !strings.HasPrefix(rootPath, "/") {
				return fmt.Errorf("storage root must be an absolute path on the storage host")
			}
			db := openDB()
			defer db.Close()
			resource, err := db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil {
				return err
			}
			if resource == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}
			existing, err := db.GetStorageTargetByName(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			target := &store.StorageTarget{ID: genID("storage_"), Name: args[0], Kind: store.StorageKindSSHRsync, ResourceID: resource.ID, RootPath: filepath.Clean(rootPath)}
			if existing != nil {
				target.ID, target.CreatedAt = existing.ID, existing.CreatedAt
			}
			if err := db.SaveStorageTarget(cmd.Context(), target); err != nil {
				return err
			}
			return printJSON(target)
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Registered resource hosting the NAS (required)")
	cmd.Flags().StringVar(&rootPath, "root", "", "Absolute dataset root on the NAS (required)")
	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("root")
	return cmd
}

func storageListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "list", Short: "List storage targets", RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		targets, err := db.ListStorageTargets(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(targets)
		}
		for _, target := range targets {
			fmt.Printf("%-20s %-12s %-12s %s\n", target.Name, target.Kind, target.Status, target.RootPath)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func storageDoctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "doctor <name>", Short: "Check NAS reachability, rsync, root access, and free space", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		target, err := db.GetStorageTargetByName(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if target == nil {
			return fmt.Errorf("storage target %s not found", args[0])
		}
		resource, err := db.GetResource(cmd.Context(), target.ResourceID)
		if err != nil {
			return err
		}
		if resource == nil {
			return fmt.Errorf("storage resource %s not found", target.ResourceID)
		}
		pool := executor.NewSSHPool(10 * time.Second)
		loadSSHKeys(pool)
		defer pool.CloseAll()
		check := fmt.Sprintf("command -v rsync >/dev/null && test -r %s && test -w %s && df -Pk %s | tail -n 1", cliShellQuote(target.RootPath), cliShellQuote(target.RootPath), cliShellQuote(target.RootPath))
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		stdout, stderr, checkErr := pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, check, resource.SocksProxy, resource.ProxyCommand)
		now := time.Now()
		target.LastCheckedAt = &now
		target.LastError = strings.TrimSpace(stderr)
		target.Status = store.StorageStatusHealthy
		if checkErr != nil {
			target.Status = store.StorageStatusUnreachable
			target.LastError = checkErr.Error()
		}
		if err := db.SaveStorageTarget(cmd.Context(), target); err != nil {
			return err
		}
		result := map[string]interface{}{"target": target, "control_plane": "aexp", "local_data_path": false, "details": strings.TrimSpace(stdout)}
		if asJSON {
			return printJSON(result)
		}
		fmt.Printf("%s: %s\n%s\n", target.Name, target.Status, strings.TrimSpace(stdout))
		if checkErr != nil {
			return checkErr
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func datasetCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dataset", Short: "Manage NAS-backed immutable datasets and compute-node caches"}
	cmd.AddCommand(datasetIngestCmd(), datasetRegisterCmd(), datasetListCmd(), datasetStatusCmd(), datasetManagedMaterializeCmd(), datasetVerifyCmd(), datasetRepairCmd(), datasetEvictCmd())
	return cmd
}

func assetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "asset",
		Short: "Publish and inspect immutable research file revisions",
		Long:  "Assets are immutable, verified file revisions used as Run inputs or published Run outputs. Dataset revisions use the same compatibility implementation.",
	}
	publish := datasetIngestCmd()
	publish.Use = "publish NAME@REVISION"
	publish.Short = "Publish and verify an immutable Asset revision"
	get := assetGetCmd()
	list := datasetListCmd()
	list.Use = "list"
	list.Short = "List published Asset revisions"
	cmd.AddCommand(publish, get, list)
	return cmd
}

func assetGetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get NAME@REVISION",
		Short: "Inspect one immutable Asset revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, revision, err := parseDatasetRef(args[0])
			if err != nil {
				return err
			}
			db := openDB()
			defer db.Close()
			asset, err := db.GetDatasetVersionByRef(cmd.Context(), name, revision)
			if err != nil {
				return err
			}
			if asset == nil {
				return fmt.Errorf("asset %s not found", args[0])
			}
			if asJSON {
				return printJSON(asset)
			}
			fmt.Printf("%s@%s  %s  %s\n", asset.DatasetID, asset.Version, asset.State, asset.LogicalURI)
			fmt.Printf("manifest %s  files %d  bytes %d\n", asset.ManifestSHA256, asset.FileCount, asset.TotalBytes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func parseDatasetRef(ref string) (string, string, error) {
	parts := strings.Split(ref, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("dataset reference must be name@version")
	}
	for _, value := range parts {
		if strings.ContainsAny(value, "/\\\n\r\t") || value == "." || value == ".." {
			return "", "", fmt.Errorf("invalid dataset reference %q", ref)
		}
	}
	return parts[0], parts[1], nil
}

func cleanRelativeDataPath(value string) (string, error) {
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\n\r\x00") {
		return "", fmt.Errorf("path must be a non-empty relative path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes the configured data root")
	}
	return cleaned, nil
}

func datasetRegisterCmd() *cobra.Command {
	var storageName, storagePath, manifestHash, archiveHash, format string
	var files, bytes int64
	cmd := &cobra.Command{Use: "register <name@version>", Short: "Register unverified metadata for a dataset already stored on the NAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name, version, err := parseDatasetRef(args[0])
		if err != nil {
			return err
		}
		relative, err := cleanRelativeDataPath(storagePath)
		if err != nil {
			return err
		}
		db := openDB()
		defer db.Close()
		target, err := db.GetStorageTargetByName(cmd.Context(), storageName)
		if err != nil {
			return err
		}
		if target == nil {
			return fmt.Errorf("storage target %s not found", storageName)
		}
		if manifestHash == "" {
			return fmt.Errorf("--manifest-sha256 is required for immutable registration; use dataset ingest to compute it automatically")
		}
		storageURI := (&url.URL{Scheme: "storage", Host: target.Name, Path: "/" + relative}).String()
		dataset := &store.DatasetVersion{ID: genID("dataset_"), DatasetID: name, Version: version, StorageTargetID: target.ID, StoragePath: relative, LogicalURI: storageURI, Revision: manifestHash, ManifestSHA256: manifestHash, ArchiveSHA256: archiveHash, Format: format, FileCount: files, TotalBytes: bytes, State: store.DatasetStateRegistered}
		stored, created, err := db.CreateDatasetVersionImmutable(cmd.Context(), dataset)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"dataset": stored, "created": created})
	}}
	cmd.Flags().StringVar(&storageName, "storage", "", "Storage target name (required)")
	cmd.Flags().StringVar(&storagePath, "path", "", "Path relative to the target root (required)")
	cmd.Flags().StringVar(&manifestHash, "manifest-sha256", "", "Dataset manifest SHA256")
	cmd.Flags().StringVar(&archiveHash, "archive-sha256", "", "Archive SHA256 when path names a single archive")
	cmd.Flags().StringVar(&format, "format", "directory", "Dataset format")
	cmd.Flags().Int64Var(&files, "files", 0, "Known file count")
	cmd.Flags().Int64Var(&bytes, "bytes", 0, "Known total bytes")
	_ = cmd.MarkFlagRequired("storage")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func datasetListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "list", Short: "List registered dataset versions", RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		datasets, err := db.ListDatasetVersions(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(datasets)
		}
		for _, d := range datasets {
			fmt.Printf("%-28s %-12s %10s  %s\n", d.DatasetID+"@"+d.Version, d.State, byteSize(d.TotalBytes), d.StoragePath)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func datasetStatusCmd() *cobra.Command {
	var resourceName string
	cmd := &cobra.Command{Use: "status <name@version>", Short: "Show dataset materialization state on a compute resource", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name, version, err := parseDatasetRef(args[0])
		if err != nil {
			return err
		}
		db := openDB()
		defer db.Close()
		dataset, err := db.GetDatasetVersionByRef(cmd.Context(), name, version)
		if err != nil {
			return err
		}
		if dataset == nil {
			return fmt.Errorf("dataset %s not found", args[0])
		}
		resource, err := db.GetResourceByName(cmd.Context(), resourceName)
		if err != nil {
			return err
		}
		if resource == nil {
			return fmt.Errorf("resource %s not found", resourceName)
		}
		m, err := db.GetDatasetMaterialization(cmd.Context(), dataset.ID, resource.ID)
		if err != nil {
			return err
		}
		return printJSON(m)
	}}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Compute resource name (required)")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

// --- mcp ---

func mcpCmd() *cobra.Command {
	var binary string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the aexp MCP server over stdio",
		Long: `Run a Model Context Protocol server over stdio.

The MCP server exposes agent-facing aexp tools while delegating execution to
the current aexp binary. Short commands use "aexp exec", which can reuse a
local "aexp serve" API process when one is already running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if binary == "" {
				exe, err := os.Executable()
				if err != nil {
					return fmt.Errorf("find current executable: %w", err)
				}
				binary = exe
			}
			return mcp.NewServer(binary).Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&binary, "binary", "", "aexp binary to wrap (defaults to the current executable)")
	cmd.AddCommand(mcpInstallCmd(false))
	cmd.AddCommand(mcpInstallCmd(true))
	return cmd
}

type mcpInstallOptions struct {
	Target      string
	Name        string
	Binary      string
	APIURL      string
	ClaudeScope string
	DryRun      bool
}

type mcpHostCommand struct {
	Target      string
	Description string
	Program     string
	Args        []string
	Optional    bool
}

func mcpInstallCmd(uninstall bool) *cobra.Command {
	opts := mcpInstallOptions{
		Target:      "all",
		Name:        "aexp",
		APIURL:      "http://127.0.0.1:8080/api/v1",
		ClaudeScope: "user",
	}
	use := "install"
	short := "Install aexp MCP config into Codex/Claude Code/Hermes"
	if uninstall {
		use = "uninstall"
		short = "Remove aexp MCP config from Codex/Claude Code/Hermes"
	}
	cmd := &cobra.Command{
		Use:     use,
		Aliases: uninstallAliases(uninstall),
		Short:   short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !uninstall {
				binary, err := resolveMCPBinary(opts.Binary)
				if err != nil {
					return err
				}
				opts.Binary = binary
			}
			plan, err := buildMCPInstallPlan(opts, uninstall)
			if err != nil {
				return err
			}
			if len(plan) == 0 {
				return fmt.Errorf("no MCP client targets selected")
			}
			for _, step := range plan {
				if opts.DryRun {
					fmt.Println(renderHostCommand(step))
					continue
				}
				if err := runMCPHostCommand(cmd.Context(), step); err != nil {
					if step.Optional {
						fmt.Fprintf(os.Stderr, "warning: %s failed: %v\n", step.Description, err)
						continue
					}
					return err
				}
				fmt.Printf("%s: %s\n", step.Target, step.Description)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Target, "target", opts.Target, "MCP client target: codex, claude, hermes, or all")
	cmd.Flags().StringVar(&opts.Name, "name", opts.Name, "MCP server name")
	cmd.Flags().StringVar(&opts.Binary, "binary", opts.Binary, "aexp binary path (install only; defaults to current executable)")
	cmd.Flags().StringVar(&opts.APIURL, "api-url", opts.APIURL, "AEXP_API_URL for the MCP server")
	cmd.Flags().StringVar(&opts.ClaudeScope, "claude-scope", opts.ClaudeScope, "Claude Code config scope: local, user, or project")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print commands without changing client config")
	return cmd
}

func uninstallAliases(uninstall bool) []string {
	if uninstall {
		return []string{"un", "remove"}
	}
	return nil
}

func resolveMCPBinary(explicit string) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(expandPath(explicit))
		if err != nil {
			return "", fmt.Errorf("resolve --binary: %w", err)
		}
		return abs, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find current executable: %w", err)
	}
	return filepath.Abs(exe)
}

func buildMCPInstallPlan(opts mcpInstallOptions, uninstall bool) ([]mcpHostCommand, error) {
	targets, err := parseMCPTargets(opts.Target)
	if err != nil {
		return nil, err
	}
	if opts.Name == "" {
		return nil, fmt.Errorf("--name is required")
	}
	if !uninstall && opts.Binary == "" {
		return nil, fmt.Errorf("--binary is required")
	}
	if opts.ClaudeScope == "" {
		opts.ClaudeScope = "user"
	}
	var plan []mcpHostCommand
	for _, target := range targets {
		switch target {
		case "codex":
			if uninstall {
				plan = append(plan, mcpHostCommand{Target: "codex", Description: "removed MCP server " + opts.Name, Program: "codex", Args: []string{"mcp", "remove", opts.Name}, Optional: true})
				continue
			}
			plan = append(plan,
				mcpHostCommand{Target: "codex", Description: "removed old MCP server " + opts.Name, Program: "codex", Args: []string{"mcp", "remove", opts.Name}, Optional: true},
				mcpHostCommand{Target: "codex", Description: "installed MCP server " + opts.Name, Program: "codex", Args: []string{"mcp", "add", opts.Name, "--env", "AEXP_API_URL=" + opts.APIURL, "--", opts.Binary, "mcp"}},
			)
		case "claude":
			if uninstall {
				plan = append(plan, mcpHostCommand{Target: "claude", Description: "removed MCP server " + opts.Name, Program: "claude", Args: []string{"mcp", "remove", opts.Name}, Optional: true})
				continue
			}
			plan = append(plan,
				mcpHostCommand{Target: "claude", Description: "removed old MCP server " + opts.Name, Program: "claude", Args: []string{"mcp", "remove", opts.Name}, Optional: true},
				mcpHostCommand{Target: "claude", Description: "installed MCP server " + opts.Name, Program: "claude", Args: []string{"mcp", "add", "--scope", opts.ClaudeScope, opts.Name, "-e", "AEXP_API_URL=" + opts.APIURL, "--", opts.Binary, "mcp"}},
			)
		case "hermes":
			optional := isImplicitAllMCPTarget(opts.Target)
			if uninstall {
				plan = append(plan, mcpHostCommand{Target: "hermes", Description: "removed MCP server " + opts.Name, Program: "hermes", Args: []string{"mcp", "remove", opts.Name}, Optional: true})
				continue
			}
			plan = append(plan,
				mcpHostCommand{Target: "hermes", Description: "removed old MCP server " + opts.Name, Program: "hermes", Args: []string{"mcp", "remove", opts.Name}, Optional: true},
				mcpHostCommand{Target: "hermes", Description: "installed MCP server " + opts.Name, Program: "hermes", Args: []string{"mcp", "add", opts.Name, "--command", "/usr/bin/env", "--args", "AEXP_API_URL=" + opts.APIURL, opts.Binary, "mcp"}, Optional: optional},
			)
		}
	}
	return plan, nil
}

func parseMCPTargets(target string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "all":
		return []string{"codex", "claude", "hermes"}, nil
	case "codex":
		return []string{"codex"}, nil
	case "claude", "claude-code", "cc":
		return []string{"claude"}, nil
	case "hermes", "hermes-agent":
		return []string{"hermes"}, nil
	default:
		return nil, fmt.Errorf("--target must be codex, claude, hermes, or all")
	}
}

func isImplicitAllMCPTarget(target string) bool {
	normalized := strings.ToLower(strings.TrimSpace(target))
	return normalized == "" || normalized == "all"
}

func runMCPHostCommand(ctx context.Context, step mcpHostCommand) error {
	if _, err := osexec.LookPath(step.Program); err != nil {
		return fmt.Errorf("%s not found in PATH", step.Program)
	}
	c := osexec.CommandContext(ctx, step.Program, step.Args...)
	out, err := c.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s: %w (%s)", renderHostCommand(step), err, msg)
		}
		return fmt.Errorf("%s: %w", renderHostCommand(step), err)
	}
	return nil
}

func renderHostCommand(step mcpHostCommand) string {
	parts := append([]string{step.Program}, step.Args...)
	return shellJoin(parts)
}

func shellJoin(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, shellQuoteArg(p))
	}
	return strings.Join(out, " ")
}

func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// --- serve ---

func serveCmd() *cobra.Command {
	var port int
	var host string
	var dbPath string
	var daemon bool
	var logPath string
	var requireTokenLocal bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the aexp server",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath = expandPath(dbPath)
			logPath = expandPath(logPath)

			if daemon && os.Getenv("AEXP_DAEMON_CHILD") == "" {
				return startServeDaemon(logPath, host, port)
			}

			// Ensure directory exists
			if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
				return fmt.Errorf("create db dir: %w", err)
			}

			db, err := store.NewSQLite(dbPath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			sshPool := executor.NewSSHPool(10 * time.Second)
			monitorPool := executor.NewSSHPool(3 * time.Second)
			loadSSHKeys(sshPool)
			loadSSHKeys(monitorPool)

			exec := executor.NewExecutor(sshPool, db)
			remoteFS := filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: sshPool}}
			fileService := filespace.NewService(db, remoteFS)
			transferPlanner := transfer.NewPlanner(db, fileService)
			transferService := transfer.NewService(db, transferPlanner)
			transferTransport := transfer.NewRsyncTransport(db, remoteFS, transfer.SSHPoolTransferRunner{Pool: sshPool})
			transferWorker := transfer.NewWorker(db, transferTransport)
			runIOService := runioservice.NewService(db, fileService, transferPlanner, transferService, transferWorker, remoteFS)
			exec.SetRunIO(runIOService)
			runIOManager := runioservice.NewManager(db, runIOService, 2*time.Second, logger)
			transferManager := transfer.NewManager(db, transferWorker, time.Second, 2, logger)
			datasetService := datasetservice.NewService(db, transferPlanner, transferService, remoteFS)
			datasetManager := datasetservice.NewManager(db, datasetService, time.Second, logger)
			mon := monitor.NewManager(db, monitorPool, 30*time.Second, logger)
			runReconciler := monitor.NewRunReconciler(db, exec, 15*time.Second, 3*time.Second, 3, logger)
			printerManager := printerservice.NewManager(db, printerservice.NewCUPS(), time.Second, logger)

			if err := mon.Start(); err != nil {
				return fmt.Errorf("start monitor: %w", err)
			}
			defer mon.Stop()
			runReconciler.Start()
			defer runReconciler.Stop()
			if err := transferManager.Start(); err != nil {
				return fmt.Errorf("start transfer manager: %w", err)
			}
			defer transferManager.Stop()
			datasetManager.Start()
			defer datasetManager.Stop()
			runIOManager.Start()
			defer runIOManager.Stop()
			if err := printerManager.Start(); err != nil {
				return fmt.Errorf("start printer manager: %w", err)
			}
			defer printerManager.Stop()

			// Generate API token
			apiToken, _ := gonanoid.Generate("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 32)
			fmt.Fprintf(os.Stderr, "\n=== API Token: %s ===\n", apiToken)
			fmt.Fprintf(os.Stderr, "Use this token in Authorization header: Bearer %s\n\n", apiToken)
			if !requireTokenLocal {
				fmt.Fprintln(os.Stderr, "Loopback requests from localhost/127.0.0.1/::1 do not require the token.")
				fmt.Fprintln(os.Stderr, "Use --require-token-local to require it for local requests too.")
				fmt.Fprintln(os.Stderr)
			}

			srv := api.NewServer(db, exec, mon, logger, apiToken, !requireTokenLocal,
				api.WithFileSpaceService(fileService), api.WithTransferServices(transferPlanner, transferService), api.WithPrinterManager(printerManager))
			handler := srv.Handler()

			addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
			logger.Info("starting aexp server", "addr", addr, "db", dbPath)
			return http.ListenAndServe(addr, handler)
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", 8080, "Server port")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Server host/interface (use 0.0.0.0 to expose on the network)")
	cmd.Flags().StringVar(&dbPath, "db", "~/.aexp/aexp.db", "Database path")
	cmd.Flags().BoolVar(&daemon, "daemon", false, "Run server in the background")
	cmd.Flags().StringVar(&logPath, "log", "~/.aexp/aexp.log", "Daemon log path")
	cmd.Flags().BoolVar(&requireTokenLocal, "require-token-local", false, "Require API token for loopback/localhost requests")

	return cmd
}

func startServeDaemon(logPath string, host string, port int) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	nullFile, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open daemon stdin: %w", err)
	}
	defer nullFile.Close()

	childArgs := stripFlag(os.Args[1:], "--daemon")
	cmd := osexec.Command(os.Args[0], childArgs...)
	cmd.Env = append(os.Environ(), "AEXP_DAEMON_CHILD=1")
	cmd.Stdin = nullFile
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	addr := net.JoinHostPort(daemonHealthHost(host), fmt.Sprintf("%d", port))
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-waitCh:
			return fmt.Errorf("daemon exited before it became reachable: %w; logs: %s", err, logPath)
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			fmt.Fprintf(os.Stderr, "aexp server started in background (pid %d)\n", cmd.Process.Pid)
			fmt.Fprintf(os.Stderr, "health: tcp://%s ok\n", addr)
			fmt.Fprintf(os.Stderr, "logs: %s\n", logPath)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon did not become reachable at %s within 5s; logs: %s", addr, logPath)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func daemonHealthHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return "127.0.0.1"
	}
	return host
}

func stripFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// --- self update / uninstall ---

type selfUpdateOptions struct {
	Repo       string
	Version    string
	Binary     string
	InstallDir string
	Name       string
	DryRun     bool
	NoChecksum bool
	StopServe  bool
	Port       int
}

func updateCmd() *cobra.Command {
	opts := selfUpdateOptions{
		Repo:    "murasame612/aexp",
		Version: "latest",
		Name:    "aexp",
		Port:    8080,
	}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download a GitHub Release and replace the local aexp binary",
		Long: `Download a GitHub Release and replace the local aexp binary.

The update is staged in a temporary directory, checksum-verified when
checksums.txt is available, smoke-tested with --version, then copied into place
through a same-directory temporary file and rename. The previous binary is kept
as a timestamped .bak file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveManagedBinaryPath(opts.Binary, opts.InstallDir, opts.Name)
			if err != nil {
				return err
			}
			asset, err := releaseAssetName(runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			archiveURL, checksumsURL := releaseAssetURLs(opts.Repo, opts.Version, asset)
			if opts.DryRun {
				fmt.Printf("target: %s\n", target)
				fmt.Printf("asset: %s\n", asset)
				fmt.Printf("download: %s\n", archiveURL)
				if !opts.NoChecksum {
					fmt.Printf("checksums: %s\n", checksumsURL)
				}
				if opts.StopServe {
					fmt.Printf("would stop listeners on tcp:%d before replacement\n", opts.Port)
				}
				return nil
			}
			tmp, err := os.MkdirTemp("", "aexp-update-*")
			if err != nil {
				return fmt.Errorf("create temp dir: %w", err)
			}
			defer os.RemoveAll(tmp)

			archivePath := filepath.Join(tmp, asset)
			fmt.Printf("Downloading %s\n", archiveURL)
			if err := downloadFile(cmd.Context(), archiveURL, archivePath); err != nil {
				return err
			}
			if !opts.NoChecksum {
				if err := verifyReleaseChecksum(cmd.Context(), checksumsURL, archivePath, asset); err != nil {
					return err
				}
			}
			candidate := filepath.Join(tmp, "aexp")
			if err := extractBinaryFromTarGz(archivePath, "aexp", candidate); err != nil {
				return err
			}
			if err := validateAexpCandidate(cmd.Context(), candidate); err != nil {
				return err
			}
			if opts.StopServe {
				if err := stopServeListenersByPort(cmd.Context(), opts.Port, false); err != nil {
					fmt.Fprintf(os.Stderr, "warning: stop serve on port %d failed: %v\n", opts.Port, err)
				}
			}
			backup, err := replaceBinary(target, candidate)
			if err != nil {
				return err
			}
			if err := ensureEventAlias(target); err != nil {
				fmt.Fprintf(os.Stderr, "warning: create aexp-event alias failed: %v\n", err)
			}
			fmt.Printf("Updated %s\n", target)
			if backup != "" {
				fmt.Printf("Backup: %s\n", backup)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Repo, "repo", opts.Repo, "GitHub repo owner/name")
	cmd.Flags().StringVar(&opts.Version, "version", opts.Version, "Release version tag or latest")
	cmd.Flags().StringVar(&opts.Binary, "binary", "", "Binary path to replace (defaults to current executable)")
	cmd.Flags().StringVar(&opts.InstallDir, "install-dir", "", "Install directory; target becomes install-dir/name")
	cmd.Flags().StringVar(&opts.Name, "name", opts.Name, "Binary name when --install-dir is used")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print update plan without downloading or replacing")
	cmd.Flags().BoolVar(&opts.NoChecksum, "no-checksum", false, "Skip checksums.txt verification")
	cmd.Flags().BoolVar(&opts.StopServe, "stop-serve", false, "Stop local aexp serve listener before replacing the binary")
	cmd.Flags().IntVar(&opts.Port, "port", opts.Port, "Port to stop when --stop-serve is set")
	return cmd
}

type uninstallOptions struct {
	Binary     string
	InstallDir string
	Name       string
	RemoveMCP  bool
	MCPTarget  string
	StopServe  bool
	Port       int
	PurgeData  bool
	Yes        bool
	DryRun     bool
}

func uninstallCmd() *cobra.Command {
	opts := uninstallOptions{
		Name:      "aexp",
		RemoveMCP: true,
		MCPTarget: "all",
		Port:      8080,
	}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the local aexp binary and MCP client config",
		Long: `Remove the local aexp binary and MCP client config.

This command does not remove ~/.aexp data by default. Use --purge-data only
when you intentionally want to delete the local database, logs, and run cache.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.DryRun && !opts.Yes {
				return fmt.Errorf("refusing to uninstall without --yes")
			}
			target, err := resolveManagedBinaryPath(opts.Binary, opts.InstallDir, opts.Name)
			if err != nil {
				return err
			}
			if opts.StopServe {
				if err := stopServeListenersByPort(cmd.Context(), opts.Port, opts.DryRun); err != nil {
					fmt.Fprintf(os.Stderr, "warning: stop serve on port %d failed: %v\n", opts.Port, err)
				}
			}
			if opts.RemoveMCP {
				plan, err := buildMCPInstallPlan(mcpInstallOptions{Target: opts.MCPTarget, Name: opts.Name}, true)
				if err != nil {
					return err
				}
				for _, step := range plan {
					if opts.DryRun {
						fmt.Println(renderHostCommand(step))
						continue
					}
					if err := runMCPHostCommand(cmd.Context(), step); err != nil {
						if step.Optional {
							fmt.Fprintf(os.Stderr, "warning: %s failed: %v\n", step.Description, err)
							continue
						}
						return err
					}
					fmt.Printf("%s: %s\n", step.Target, step.Description)
				}
			}
			for _, path := range uninstallBinaryPaths(target) {
				if opts.DryRun {
					fmt.Printf("rm %s\n", path)
					continue
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove %s: %w", path, err)
				}
				fmt.Printf("Removed %s\n", path)
			}
			if opts.PurgeData {
				dataDir := expandPath("~/.aexp")
				if opts.DryRun {
					fmt.Printf("rm -rf %s\n", dataDir)
				} else if err := os.RemoveAll(dataDir); err != nil {
					return fmt.Errorf("remove %s: %w", dataDir, err)
				} else {
					fmt.Printf("Removed %s\n", dataDir)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Binary, "binary", "", "Binary path to remove (defaults to current executable)")
	cmd.Flags().StringVar(&opts.InstallDir, "install-dir", "", "Install directory; target becomes install-dir/name")
	cmd.Flags().StringVar(&opts.Name, "name", opts.Name, "Binary/MCP server name")
	cmd.Flags().BoolVar(&opts.RemoveMCP, "mcp", opts.RemoveMCP, "Remove MCP config from selected clients")
	cmd.Flags().StringVar(&opts.MCPTarget, "mcp-target", opts.MCPTarget, "MCP target: codex, claude, hermes, or all")
	cmd.Flags().BoolVar(&opts.StopServe, "stop-serve", false, "Stop local aexp serve listener before uninstalling")
	cmd.Flags().IntVar(&opts.Port, "port", opts.Port, "Port to stop when --stop-serve is set")
	cmd.Flags().BoolVar(&opts.PurgeData, "purge-data", false, "Also remove ~/.aexp data")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "Confirm uninstall")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print uninstall plan without removing anything")
	return cmd
}

func resolveManagedBinaryPath(explicit, installDir, name string) (string, error) {
	if installDir != "" {
		if strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("--name is required with --install-dir")
		}
		return filepath.Abs(filepath.Join(expandPath(installDir), name))
	}
	if explicit != "" {
		return filepath.Abs(expandPath(explicit))
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find current executable: %w", err)
	}
	return filepath.Abs(exe)
}

func releaseAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("unsupported OS %q", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
	return fmt.Sprintf("aexp_%s_%s.tar.gz", goos, goarch), nil
}

func releaseAssetURLs(repo, version, asset string) (string, string) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	version = strings.TrimSpace(version)
	if version == "" || version == "latest" {
		base := "https://github.com/" + repo + "/releases/latest/download/"
		return base + asset, base + "checksums.txt"
	}
	base := "https://github.com/" + repo + "/releases/download/" + version + "/"
	return base + asset, base + "checksums.txt"
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "aexp/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

func verifyReleaseChecksum(ctx context.Context, checksumsURL, archivePath, asset string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "aexp/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download checksums: HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	expected := checksumForAsset(string(body), asset)
	if expected == "" {
		return fmt.Errorf("checksums.txt did not contain %s", asset)
	}
	actual, err := sha256File(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, expected, actual)
	}
	fmt.Println("Checksum ok")
	return nil
}

func checksumForAsset(checksums, asset string) string {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[len(fields)-1] == asset {
			return fields[0]
		}
	}
	return ""
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractBinaryFromTarGz(archivePath, memberName, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(header.Name) != memberName || header.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return fmt.Errorf("create %s: %w", dest, err)
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %s: %w", memberName, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", dest, closeErr)
		}
		return os.Chmod(dest, 0755)
	}
	return fmt.Errorf("archive did not contain executable %s", memberName)
}

func validateAexpCandidate(ctx context.Context, candidate string) error {
	cmd := osexec.CommandContext(ctx, candidate, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("candidate failed --version: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func replaceBinary(target, candidate string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("create install dir: %w", err)
	}
	backup := ""
	if _, err := os.Stat(target); err == nil {
		backup = target + ".bak." + time.Now().Format("20060102150405")
		if err := os.Rename(target, backup); err != nil {
			return "", fmt.Errorf("backup existing binary: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", target, err)
	}
	tmp := target + ".tmp." + genID("")
	if err := copyFileMode(candidate, tmp, 0755); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return "", fmt.Errorf("install binary: %w", err)
	}
	return backup, nil
}

func copyFileMode(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dest, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", dest, closeErr)
	}
	return os.Chmod(dest, mode)
}

func ensureEventAlias(target string) error {
	dir := filepath.Dir(target)
	name := filepath.Base(target)
	alias := filepath.Join(dir, "aexp-event")
	_ = os.Remove(alias)
	return os.Symlink(name, alias)
}

func uninstallBinaryPaths(target string) []string {
	paths := []string{target}
	alias := filepath.Join(filepath.Dir(target), "aexp-event")
	if alias != target {
		paths = append(paths, alias)
	}
	return paths
}

func stopServeListenersByPort(ctx context.Context, port int, dryRun bool) error {
	if port <= 0 {
		return fmt.Errorf("invalid port %d", port)
	}
	if _, err := osexec.LookPath("lsof"); err != nil {
		return fmt.Errorf("lsof not found")
	}
	args := []string{"-tiTCP:" + strconv.Itoa(port), "-sTCP:LISTEN"}
	if dryRun {
		fmt.Printf("lsof %s | xargs kill\n", strings.Join(args, " "))
		return nil
	}
	out, err := osexec.CommandContext(ctx, "lsof", args...).Output()
	if err != nil {
		if strings.TrimSpace(string(out)) == "" {
			return nil
		}
		return fmt.Errorf("list listeners: %w", err)
	}
	for _, line := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid == os.Getpid() {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

// --- agent ---

func agentCmd() *cobra.Command {
	var asJSON bool

	steps, rules := agentCardContent()

	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Show the short operational card for agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				return printJSON(map[string]interface{}{
					"title": "AEXP Agent Card",
					"steps": steps,
					"rules": rules,
				})
			}
			fmt.Println("AEXP Agent Card")
			fmt.Println()
			for i, step := range steps {
				parts := strings.SplitN(step, ": ", 2)
				fmt.Printf("%d. %s:\n", i+1, parts[0])
				if len(parts) == 2 {
					fmt.Printf("   %s\n", parts[1])
				}
				fmt.Println()
			}
			fmt.Println("Rules:")
			for _, rule := range rules {
				fmt.Printf("- %s\n", rule)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func agentCardContent() ([]string, []string) {
	steps := []string{
		"Check resources: aexp resource list --verbose",
		"Read current project memory: aexp project journal list <project_id> --next-action-status open --json",
		"Inspect remote: aexp exec --resource <name> --cwd <path> --project-env auto -- 'pwd; python -V; nvidia-smi'",
		"Instrument training code: import metric/progress/param/note from aexp_events before submitting; telemetry is written during the run, not reconstructed afterward",
		"Submit formal run: aexp run submit --resource <name> --kind formal --name <run-name> --cwd <project> --conda-env <env> --gpu-index 0 --metric-paths <metrics.json> --log-paths '<logs/**/*>' -- python train.py ...",
		"Submit setup task: aexp run submit --resource <name> --kind setup --no-gpu --cwd <project> --shell -- 'python -m pip install -r requirements.txt'",
		"Monitor run: aexp run snapshot <run_id> --json; use aexp run events <run_id> --tail 50 --json for raw structured events",
		"Debug failures: aexp run status <run_id> --short; aexp run logs <run_id> --tail 100 --no-follow",
		"Preserve reasoning: aexp project journal create <project_id> --title ... --body-md-file notes.md --run <run_id> --next-action ...",
		"Route research evidence: aexp evidence list --project <project_id> --status active --json; choose a clearly matching topic graph or use the primary graph",
	}
	rules := []string{
		"aexp runs locally; do not ssh aexp. Use aexp exec/run to dispatch to registered resources.",
		"Use run submit for experiments; use exec only for inspection/ops.",
		"For project checks, prefer exec --project-env auto so .venv or resource conda_env is activated when available.",
		"Use --kind formal for paper evidence; never treat setup/smoke runs as real results.",
		"Training metrics/progress/params belong in aexp_events.py calls inside the training/eval script. Do not emit manual post-hoc event metrics.",
		"Use short metric names such as train/loss and val/loss; put trial, variant, split, stage, seed, and fold in fields. In sweeps, epoch is trial-local.",
		"For active training, monitor structured UI events first with run snapshot/events/metrics; avoid tight status/log polling. Poll every 30-60s, then back off up to 120s when nothing changes.",
		"Always provide --metric-paths and --log-paths for formal runs.",
		"--cwd must be under the resource root_dir; update root_dir if the project lives elsewhere.",
		"After interpreting logs/metrics/artifacts, append a Project journal entry. Linking the Run is optional; use Run marks only for legacy compatibility.",
		"For project-organized work, use .aexp.yaml project.id. Do not create new Run marks or Project cards for research reasoning.",
		"Before proposing evidence, list the Project's active graphs. Use a topic graph only when its purpose or routing hints clearly match; otherwise use the primary graph. Explain explicit topic routing with --routing-reason.",
	}
	return steps, rules
}

// --- doctor ---

type doctorCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
	Detail   string `json:"detail,omitempty"`
}

type doctorReport struct {
	Resource                 string                `json:"resource"`
	Cwd                      string                `json:"cwd"`
	CondaEnv                 string                `json:"conda_env"`
	Project                  *store.ProjectProfile `json:"project,omitempty"`
	ProjectConfig            string                `json:"project_config,omitempty"`
	Events                   doctorEvents          `json:"events"`
	Checks                   []doctorCheck         `json:"checks"`
	RecommendedSubmitCommand string                `json:"recommended_submit_command"`
	Recommended              []string              `json:"recommended,omitempty"`
	RecommendedFixes         []string              `json:"recommended_fixes,omitempty"`
	Recipes                  []doctorRecipe        `json:"recipes,omitempty"`
}

type doctorEvents struct {
	Ready              bool     `json:"ready"`
	UIEvents           string   `json:"ui_events"`
	Helper             string   `json:"helper"`
	ProjectHelper      string   `json:"project_helper,omitempty"`
	RecommendedImports []string `json:"recommended_imports"`
	Monitor            []string `json:"monitor"`
}

type doctorRecipe struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Evidence    string `json:"evidence"`
	CommandOK   bool   `json:"command_ok"`
	Selected    bool   `json:"selected,omitempty"`
	Recommended string `json:"recommended,omitempty"`
}

func doctorCmd() *cobra.Command {
	var resourceName, cwd, condaEnv string
	var gpuIndex int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check whether a resource/cwd/conda environment is ready for runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resourceName == "" {
				return fmt.Errorf("--resource is required")
			}
			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}
			if cwd == "" {
				cwd = res.RootDir
			}
			if condaEnv == "" {
				condaEnv = res.CondaEnv
			}

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)
			report := runDoctorChecks(cmd.Context(), sshPool, res, cwd, condaEnv, gpuIndex)
			if err := updateResourceControlStatus(cmd.Context(), db, res, report); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to update resource ssh status: %v\n", err)
			}
			if report.Project != nil {
				if err := db.SaveProjectProfile(cmd.Context(), report.Project); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to save project profile: %v\n", err)
				}
			}

			if asJSON {
				return printJSON(report)
			}
			printDoctorReport(report)
			for _, check := range report.Checks {
				if !check.OK && check.Severity != "warn" {
					return fmt.Errorf("doctor failed: %s", check.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project working directory to validate (defaults to resource root_dir)")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment to validate (defaults to resource conda_env)")
	cmd.Flags().IntVar(&gpuIndex, "gpu-index", 0, "GPU index used in the recommended submit command")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func runDoctorChecks(ctx context.Context, pool *executor.SSHPool, res *store.Resource, cwd, condaEnv string, gpuIndex int) doctorReport {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	report := doctorReport{
		Resource: res.Name,
		Cwd:      cwd,
		CondaEnv: condaEnv,
		Events:   defaultDoctorEvents(""),
	}
	add := func(name string, ok bool, detail string) int {
		severity := "fail"
		if ok {
			severity = "ok"
		}
		report.Checks = append(report.Checks, doctorCheck{Name: name, OK: ok, Severity: severity, Detail: detail})
		return len(report.Checks) - 1
	}
	execRemote := func(command string) (string, string, error) {
		return pool.Exec(ctx, res.Host, res.Port, res.User, res.AuthRef, executor.WithResourceRemotePath(res, command), res.SocksProxy, res.ProxyCommand)
	}
	remoteOK := 0

	out, stderr, err := retryRemote(3, 300*time.Millisecond, func() (string, string, error) {
		return execRemote("echo AEXP_OK")
	})
	reachableOK := err == nil && strings.Contains(out, "AEXP_OK")
	if reachableOK {
		remoteOK++
	}
	reachableIdx := add("resource reachable", reachableOK, strings.TrimSpace(firstNonEmpty(stderr, errString(err))))

	root := res.RootDir
	if root == "" {
		root = "/"
	}
	if cwdEscapesRoot(root, cwd) {
		add("cwd under root_dir", false, fmt.Sprintf("cwd %q escapes root_dir %q", cwd, root))
	} else {
		add("cwd under root_dir", true, fmt.Sprintf("root_dir=%s", root))
	}

	out, stderr, err = execRemote("test -d " + cliShellQuote(cwd) + " && echo OK")
	checkOK := err == nil && strings.Contains(out, "OK")
	if checkOK {
		remoteOK++
	}
	add("cwd exists", checkOK, strings.TrimSpace(firstNonEmpty(stderr, errString(err))))

	detector := executor.NewExecutor(pool, nil)
	profile, detectErr := detector.DetectProject(ctx, res, cwd, executor.ProjectEnvAuto, condaEnv)
	if detectErr != nil {
		add("project env detect", false, detectErr.Error())
		report.RecommendedFixes = append(report.RecommendedFixes, "Fix cwd/resource access, then run: aexp project detect --resource "+cliShellQuote(res.Name)+" --cwd "+cliShellQuote(cwd))
	} else {
		report.Project = profile
		remoteOK++
		add("project env resolved", profile.PythonOK, fmt.Sprintf("%s python=%s", profile.ResolvedEnv, profile.Python))
		add("torch import", profile.TorchOK, boolDetail(profile.TorchOK, "torch ok", "torch unavailable in resolved python"))
		if !profile.TorchOK {
			report.Checks[len(report.Checks)-1].Severity = "warn"
		}
		add("cuda visible", profile.CUDAOK, profile.CUDA)
		if !profile.CUDAOK {
			report.Checks[len(report.Checks)-1].Severity = "warn"
		}
		for _, warning := range profile.Warnings {
			idx := add("project warning", false, warning)
			report.Checks[idx].Severity = "warn"
		}
		if profile.ResolvedEnv == executor.ProjectEnvRaw {
			report.RecommendedFixes = append(report.RecommendedFixes, "python missing in project env; create .venv or set resource conda_env, then use --project-env auto")
			if fix := detectResourceEnvFix(execRemote, res.Name); fix != "" {
				report.RecommendedFixes = append(report.RecommendedFixes, fix)
			}
		}
	}

	out, stderr, err = execRemote("command -v tmux >/dev/null 2>&1 && tmux -V")
	checkOK = err == nil && strings.TrimSpace(out) != ""
	if checkOK {
		remoteOK++
	}
	add("tmux available", checkOK, strings.TrimSpace(firstNonEmpty(out, stderr, errString(err))))

	writeCmd := "cd " + cliShellQuote(cwd) + " && f=.aexp_write_test_$$ && touch \"$f\" && rm \"$f\" && echo OK"
	out, stderr, err = execRemote(writeCmd)
	checkOK = err == nil && strings.Contains(out, "OK")
	if checkOK {
		remoteOK++
	}
	add("write permission ok", checkOK, strings.TrimSpace(firstNonEmpty(stderr, errString(err))))

	if !report.Checks[reachableIdx].OK && remoteOK > 0 {
		report.Checks[reachableIdx].OK = true
		report.Checks[reachableIdx].Severity = "warn"
		report.Checks[reachableIdx].Detail = "transient ssh failure; later remote checks succeeded. " + report.Checks[reachableIdx].Detail
	}

	report.RecommendedSubmitCommand = recommendedSubmitCommand(res.Name, cwd, condaEnv, gpuIndex)
	return report
}

func detectResourceEnvFix(execRemote func(string) (string, string, error), resourceName string) string {
	if execRemote == nil {
		return ""
	}
	out, _, err := execRemote(resourceEnvProbeScript())
	if err != nil {
		return ""
	}
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "|")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	remotePath := values["remote_path"]
	if remotePath == "" {
		return ""
	}
	parts := []string{"aexp resource update", cliShellQuote(resourceName), "--remote-path", cliShellQuote(remotePath)}
	if values["conda_base"] != "" {
		parts = append(parts, "--conda-base", cliShellQuote(values["conda_base"]))
	}
	if values["conda_init"] != "" {
		parts = append(parts, "--conda-init", cliShellQuote(values["conda_init"]))
	}
	if values["conda_env"] != "" {
		parts = append(parts, "--conda-env", cliShellQuote(values["conda_env"]))
	}
	return "detected usable Python/Conda; persist it with: " + strings.Join(parts, " ")
}

func resourceEnvProbeScript() string {
	return `set +e
CONDA_EXE=""
for conda_bin in conda /opt/conda/bin/conda "$HOME/miniforge3/bin/conda" "$HOME/miniconda3/bin/conda" "$HOME/anaconda3/bin/conda"; do
  if command -v "$conda_bin" >/dev/null 2>&1 || [ -x "$conda_bin" ]; then
    CONDA_EXE="$conda_bin"
    break
  fi
done
if [ -n "$CONDA_EXE" ]; then
  CONDA_BASE="$("$CONDA_EXE" info --base 2>/dev/null)"
  if [ -n "$CONDA_BASE" ]; then
    echo "remote_path|$CONDA_BASE/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    echo "conda_base|$CONDA_BASE"
    if [ -f "$CONDA_BASE/etc/profile.d/conda.sh" ]; then
      echo "conda_init|$CONDA_BASE/etc/profile.d/conda.sh"
    fi
    if "$CONDA_BASE/bin/python" -V >/dev/null 2>&1; then
      echo "conda_env|base"
    else
      "$CONDA_EXE" env list 2>/dev/null | awk 'NF >= 2 && $1 !~ /^#/ { print "conda_env|" $1; exit }'
    fi
    exit 0
  fi
fi
for py in /opt/conda/bin/python "$HOME/miniforge3/bin/python" "$HOME/miniconda3/bin/python" "$HOME/anaconda3/bin/python" /usr/bin/python3 /usr/bin/python; do
  if [ -x "$py" ]; then
    bin_dir="$(dirname "$py")"
    echo "remote_path|$bin_dir:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    exit 0
  fi
done`
}

func defaultDoctorEvents(projectHelper string) doctorEvents {
	events := doctorEvents{
		Ready:              true,
		UIEvents:           ".aexp/events/<run_id>.jsonl (default unless --ui-events off)",
		Helper:             "run submit injects aexp_events.py into $AEXP_RUN_DIR and exports PYTHONPATH",
		RecommendedImports: []string{"from aexp_events import metric, training_epoch, training_done, progress, param, note"},
		Monitor:            []string{"aexp run snapshot <run_id> --json", "aexp run events <run_id> --tail 50 --json", "aexp run metrics <run_id> --latest --json"},
	}
	if projectHelper != "" {
		events.ProjectHelper = projectHelper
	}
	return events
}

func updateResourceControlStatus(ctx context.Context, db store.Store, res *store.Resource, report doctorReport) error {
	now := time.Now()
	res.LastCheckedAt = &now
	reachable := doctorCheckByName(report, "resource reachable")
	if reachable != nil && (reachable.OK || reachable.Severity == "warn") {
		res.SSHStatus = store.ResourceSSHStatusOK
		res.LastDoctorError = ""
		res.LastSuccessAt = &now
	} else {
		res.SSHStatus = store.ResourceSSHStatusFailed
		if reachable != nil && strings.TrimSpace(reachable.Detail) != "" {
			res.LastDoctorError = reachable.Detail
		} else {
			res.LastDoctorError = firstDoctorFailureDetail(report)
		}
	}
	return db.UpdateResource(ctx, res)
}

func doctorCheckByName(report doctorReport, name string) *doctorCheck {
	for i := range report.Checks {
		if report.Checks[i].Name == name {
			return &report.Checks[i]
		}
	}
	return nil
}

func firstDoctorFailureDetail(report doctorReport) string {
	for _, check := range report.Checks {
		if !check.OK && check.Severity != "warn" && strings.TrimSpace(check.Detail) != "" {
			return check.Detail
		}
	}
	return "resource control channel failed"
}

func retryRemote(attempts int, delay time.Duration, fn func() (string, string, error)) (string, string, error) {
	var stdout, stderr string
	var err error
	for i := 0; i < attempts; i++ {
		stdout, stderr, err = fn()
		if err == nil {
			return stdout, stderr, nil
		}
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return stdout, stderr, err
}

func remoteCondaPrefix(res *store.Resource, env string) string {
	if env == "" {
		return ""
	}
	init := res.CondaInit
	if init == "" && res.CondaBase != "" {
		init = strings.TrimRight(res.CondaBase, "/") + "/etc/profile.d/conda.sh"
	}
	if init != "" {
		return "source " + cliShellQuote(init) + " && conda activate " + cliShellQuote(env) + " && "
	}
	return "conda activate " + cliShellQuote(env) + " && "
}

func cwdEscapesRoot(rootDir, cwd string) bool {
	if cwd == "" || rootDir == "" {
		return false
	}
	resolved := cwd
	if !strings.HasPrefix(cwd, "/") {
		resolved = strings.TrimRight(rootDir, "/") + "/" + cwd
	}
	cleanRoot := path.Clean(rootDir)
	cleanCwd := path.Clean(resolved)
	if cleanRoot == "/" {
		return !strings.HasPrefix(cleanCwd, "/")
	}
	return cleanCwd != cleanRoot && !strings.HasPrefix(cleanCwd, cleanRoot+"/")
}

func recommendedSubmitCommand(resourceName, cwd, condaEnv string, gpuIndex int) string {
	parts := []string{
		"aexp run submit",
		"--resource " + cliShellQuote(resourceName),
		"--kind formal",
		"--name <run-name>",
		"--cwd " + cliShellQuote(cwd),
		"--project-env auto",
	}
	if condaEnv != "" {
		parts = append(parts, "--conda-env "+cliShellQuote(condaEnv))
	}
	parts = append(parts,
		fmt.Sprintf("--gpu-index %d", gpuIndex),
		"--metric-paths '<metrics.json>'",
		"--log-paths '<logs/**/*>'",
		"-- python train.py ...",
	)
	return strings.Join(parts, " ")
}

func boolDetail(ok bool, okText, failText string) string {
	if ok {
		return okText
	}
	return failText
}

func printDoctorReport(report doctorReport) {
	fmt.Println("AEXP Doctor")
	fmt.Println()
	if report.ProjectConfig != "" {
		fmt.Printf("project:  %s\n", report.ProjectConfig)
	}
	fmt.Printf("resource: %s\n", report.Resource)
	fmt.Printf("cwd:      %s\n", report.Cwd)
	if report.CondaEnv != "" {
		fmt.Printf("conda:    %s\n", report.CondaEnv)
	}
	if report.Project != nil {
		fmt.Printf("env:      %s\n", report.Project.ResolvedEnv)
		fmt.Printf("python:   %s\n", report.Project.Python)
	}
	fmt.Println()
	for _, check := range report.Checks {
		status := "FAIL"
		switch check.Severity {
		case "warn":
			status = "WARN"
		case "ok":
			status = "OK"
		}
		if check.Detail != "" {
			fmt.Printf("[%s] %s - %s\n", status, check.Name, check.Detail)
		} else {
			fmt.Printf("[%s] %s\n", status, check.Name)
		}
	}
	fmt.Println()
	if len(report.RecommendedFixes) > 0 {
		fmt.Println("fix:")
		for _, fix := range report.RecommendedFixes {
			fmt.Println(fix)
		}
		fmt.Println()
	}
	if report.Events.Ready {
		fmt.Println("structured events:")
		fmt.Printf("ui_events: %s\n", report.Events.UIEvents)
		fmt.Printf("helper:    %s\n", report.Events.Helper)
		if report.Events.ProjectHelper != "" {
			fmt.Printf("project helper: %s\n", report.Events.ProjectHelper)
		}
		if len(report.Events.RecommendedImports) > 0 {
			fmt.Printf("python:    %s\n", strings.Join(report.Events.RecommendedImports, "; "))
		}
		fmt.Println()
	}
	if len(report.Recommended) > 0 {
		fmt.Println("recommended:")
		for _, command := range report.Recommended {
			fmt.Println(command)
		}
		return
	}
	fmt.Println("recommended submit command:")
	fmt.Println(report.RecommendedSubmitCommand)
}

func applyProjectDoctorConfigRecommendations(report *doctorReport, cfg *projectFileConfig, recipeName string) {
	report.ProjectConfig = cfg.Path
	report.Events = defaultDoctorEvents(projectDoctorProjectHelper(cfg))
	for _, warning := range cfg.Warnings {
		report.Checks = append(report.Checks, doctorCheck{Name: "project config warning", OK: false, Severity: "warn", Detail: warning})
	}
	if recipeName == "" {
		recipeName = defaultProjectRecipeName(cfg)
	}
	if recipeName == "" {
		report.RecommendedSubmitCommand = "aexp project run <recipe> --dry-run"
		report.Recommended = []string{"aexp project run <recipe> --dry-run"}
		report.RecommendedFixes = append(report.RecommendedFixes, "no project recipes found; add train/setup recipes to "+cliShellQuote(cfg.Path))
		report.Recipes = projectDoctorRecipeReports(cfg, "")
		applyProjectDoctorConfigIssues(report, cfg)
		return
	}
	if _, ok := cfg.Commands[recipeName]; !ok {
		report.RecommendedFixes = append(report.RecommendedFixes, "recipe "+cliShellQuote(recipeName)+" not found in "+cliShellQuote(cfg.Path))
		report.Recipes = projectDoctorRecipeReports(cfg, recipeName)
		applyProjectDoctorConfigIssues(report, cfg)
		return
	}
	report.RecommendedSubmitCommand = joinNonEmpty(" ", "aexp project run", cliShellQuote(recipeName), projectConfigFlagForRecommendation(cfg.Path))
	report.Recipes = projectDoctorRecipeReports(cfg, recipeName)
	report.Recommended = projectDoctorRecommendedCommands(cfg, recipeName)
	applyProjectDoctorConfigIssues(report, cfg)
}

func projectDoctorProjectHelper(cfg *projectFileConfig) string {
	if cfg == nil || cfg.Path == "" {
		return ""
	}
	helperPath := filepath.Join(filepath.Dir(cfg.Path), "aexp_events.py")
	if _, err := os.Stat(helperPath); err == nil {
		return helperPath
	}
	return "missing in repo; run 'aexp project init' to create a local helper, or rely on run-time injection"
}

func printProjectDoctorRecipes(cfg *projectFileConfig, selected string) {
	fmt.Println()
	fmt.Println("project recipes:")
	names := sortedProjectRecipeNames(cfg)
	if len(names) == 0 {
		fmt.Println("  none")
		fmt.Println("  add a recipe such as:")
		fmt.Println("    train:")
		fmt.Println("      command: bash scripts/train.sh configs/experiment.yaml")
		fmt.Println("      kind: formal")
		return
	}
	if selected == "" {
		selected = defaultProjectRecipeName(cfg)
	}
	for _, name := range names {
		entry := cfg.Commands[name]
		kind := entry.Kind
		if kind == "" {
			kind = store.RunKindFormal
		}
		marker := " "
		if name == selected {
			marker = "*"
		}
		fmt.Printf("%s %s: kind=%s, %s\n", marker, name, kind, projectKindEvidenceLabel(kind))
		if strings.TrimSpace(entry.Command) == "" {
			fmt.Println("    warning: missing command")
		}
	}
}

func projectDoctorRecipeReports(cfg *projectFileConfig, selected string) []doctorRecipe {
	names := sortedProjectRecipeNames(cfg)
	recipes := make([]doctorRecipe, 0, len(names))
	for _, name := range names {
		entry := cfg.Commands[name]
		kind := entry.Kind
		if kind == "" {
			kind = store.RunKindFormal
		}
		recipes = append(recipes, doctorRecipe{
			Name:        name,
			Kind:        kind,
			Evidence:    projectKindEvidenceLabel(kind),
			CommandOK:   strings.TrimSpace(entry.Command) != "",
			Selected:    name == selected,
			Recommended: joinNonEmpty(" ", "aexp project run", cliShellQuote(name), projectConfigFlagForRecommendation(cfg.Path), "--dry-run"),
		})
	}
	return recipes
}

func projectDoctorRecommendedCommands(cfg *projectFileConfig, selected string) []string {
	var recommended []string
	if cfg.Sync.Source != "" || cfg.Sync.Target != "" || cfg.Cwd != "" {
		recommended = append(recommended, joinNonEmpty(" ", "aexp project sync", projectConfigFlagForRecommendation(cfg.Path), "--dry-run"))
	}
	if _, ok := cfg.Commands["setup"]; ok {
		recommended = append(recommended, joinNonEmpty(" ", "aexp project run", "setup", projectConfigFlagForRecommendation(cfg.Path), "--dry-run"))
	}
	if selected != "" {
		recommended = append(recommended,
			joinNonEmpty(" ", "aexp project run", cliShellQuote(selected), projectConfigFlagForRecommendation(cfg.Path), "--dry-run"),
			joinNonEmpty(" ", "aexp project run", cliShellQuote(selected), projectConfigFlagForRecommendation(cfg.Path)),
		)
	}
	return dedupeStrings(recommended)
}

func applyProjectDoctorConfigIssues(report *doctorReport, cfg *projectFileConfig) {
	if cfg.Resource == "" {
		report.RecommendedFixes = append(report.RecommendedFixes, "set resource: in "+cliShellQuote(cfg.Path)+" or pass --resource")
	}
	if cfg.Cwd == "" {
		report.RecommendedFixes = append(report.RecommendedFixes, "set cwd: in "+cliShellQuote(cfg.Path)+" so project recipes run in the intended directory")
	}
	if len(cfg.Commands) == 0 {
		report.RecommendedFixes = append(report.RecommendedFixes, "add at least one recipe such as train: {command, kind: formal}")
	}
	for _, recipe := range report.Recipes {
		if !recipe.CommandOK {
			report.RecommendedFixes = append(report.RecommendedFixes, "recipe "+cliShellQuote(recipe.Name)+" is missing command")
		}
	}
	if !doctorCheckPassed(report, "cwd exists") && (cfg.Sync.Target != "" || cfg.Cwd != "") {
		report.RecommendedFixes = append(report.RecommendedFixes,
			"cwd missing on remote; use: "+joinNonEmpty(" ", "aexp project sync", projectConfigFlagForRecommendation(cfg.Path), "--dry-run"),
			"cwd missing on remote; then: "+joinNonEmpty(" ", "aexp project sync", projectConfigFlagForRecommendation(cfg.Path)),
		)
	}
	report.RecommendedFixes = dedupeStrings(report.RecommendedFixes)
}

func doctorCheckPassed(report *doctorReport, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.OK || check.Severity == "warn"
		}
	}
	return false
}

func defaultProjectRecipeName(cfg *projectFileConfig) string {
	for _, name := range []string{"train", "formal", "run"} {
		if _, ok := cfg.Commands[name]; ok {
			return name
		}
	}
	names := sortedProjectRecipeNames(cfg)
	for _, name := range names {
		kind := cfg.Commands[name].Kind
		if kind != store.RunKindSetup && kind != store.RunKindSmoke {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func sortedProjectRecipeNames(cfg *projectFileConfig) []string {
	names := make([]string, 0, len(cfg.Commands))
	for name := range cfg.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func projectConfigFlagForRecommendation(path string) string {
	if filepath.Base(path) == ".aexp.yaml" {
		return ""
	}
	return "--config " + cliShellQuote(path)
}

func joinNonEmpty(sep string, values ...string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, sep)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- project ---

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Inspect Projects and validate their runtime configuration",
		Long: `A Project is the common scope for Assets, Runs, and one primary
Evidence Map. Runtime detection and recipes remain Project operations.`,
	}
	cmd.AddCommand(projectListDefinitionsCmd())
	cmd.AddCommand(projectGetDefinitionCmd())
	cmd.AddCommand(projectDetectCmd())
	cmd.AddCommand(projectDoctorCmd())
	cmd.AddCommand(projectInitCmd())
	cmd.AddCommand(projectRunCmd())
	cmd.AddCommand(projectJournalCmd())
	cmd.AddCommand(projectSyncCmd())
	cmd.AddCommand(projectCardCmd())
	cmd.AddCommand(projectRunsCmd())
	cmd.AddCommand(projectDigestCmd())
	for _, command := range cmd.Commands() {
		switch command.Name() {
		case "card", "digest", "runs", "sync":
			command.Hidden = true
		}
	}
	return cmd
}

func projectJournalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Write and read the Project work journal",
		Long: `The Project Journal is the low-friction reasoning layer between Runs and
curated Evidence Maps. Entries are append-only, may reference zero or more Runs,
and may carry one explicit next action.`,
	}
	cmd.AddCommand(projectJournalCreateCmd())
	cmd.AddCommand(projectJournalListCmd())
	cmd.AddCommand(projectJournalShowCmd())
	cmd.AddCommand(projectJournalNextActionCmd())
	return cmd
}

func projectJournalCreateCmd() *cobra.Command {
	var actor, title, bodyMD, bodyMDFile, nextAction string
	var runIDs []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create PROJECT_ID",
		Short: "Append an entry to a Project journal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if bodyMDFile != "" {
				data, err := os.ReadFile(expandPath(bodyMDFile))
				if err != nil {
					return fmt.Errorf("read body md file: %w", err)
				}
				bodyMD = string(data)
			}
			db := openDB()
			defer db.Close()
			entry := store.ProjectJournalEntry{
				ID:         genID("journal_"),
				ProjectID:  args[0],
				Actor:      actor,
				Title:      title,
				BodyMD:     bodyMD,
				NextAction: nextAction,
				RunIDs:     runIDs,
			}
			if err := db.CreateProjectJournalEntry(cmd.Context(), &entry); err != nil {
				return err
			}
			if asJSON {
				return printJSON(entry)
			}
			fmt.Printf("Journaled %s in project %s\n", entry.ID, entry.ProjectID)
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "agent", "Actor writing the entry")
	cmd.Flags().StringVar(&title, "title", "", "Short entry title")
	cmd.Flags().StringVar(&bodyMD, "body-md", "", "Markdown body")
	cmd.Flags().StringVar(&bodyMDFile, "body-md-file", "", "Read Markdown body from a file")
	cmd.Flags().StringVar(&nextAction, "next-action", "", "One concrete next action")
	cmd.Flags().StringSliceVar(&runIDs, "run", nil, "Related Run id; repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func projectJournalListCmd() *cobra.Command {
	var runID, query, nextActionStatus string
	var limit, offset int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list PROJECT_ID",
		Short: "List a Project journal newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			entries, err := db.ListProjectJournalEntries(cmd.Context(), store.ProjectJournalFilter{
				ProjectID: args[0], RunID: runID, Query: query,
				NextActionStatus: nextActionStatus, Limit: limit, Offset: offset,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No project journal entries found.")
				return nil
			}
			fmt.Printf("%-14s %-18s %-10s %-36s %s\n", "TIME", "ENTRY_ID", "ACTOR", "TITLE", "RUNS")
			for _, entry := range entries {
				fmt.Printf("%-14s %-18s %-10s %-36s %s\n",
					entry.CreatedAt.Format("01-02 15:04"),
					truncStr(entry.ID, 18),
					truncStr(entry.Actor, 10),
					truncStr(entry.Title, 36),
					strings.Join(entry.RunIDs, ","),
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "Filter by related Run id")
	cmd.Flags().StringVar(&query, "query", "", "Search title, body, and next action")
	cmd.Flags().StringVar(&nextActionStatus, "next-action-status", "", "Filter next action: none, open, or done")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum entries")
	cmd.Flags().IntVar(&offset, "offset", 0, "Entries to skip")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func projectJournalShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show ENTRY_ID",
		Short: "Show one Project journal entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			entry, err := db.GetProjectJournalEntry(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if entry == nil {
				return fmt.Errorf("journal entry %s not found", args[0])
			}
			if asJSON {
				return printJSON(entry)
			}
			fmt.Printf("%s\n\n%s\n", entry.Title, entry.BodyMD)
			if entry.NextAction != "" {
				fmt.Printf("\nNext (%s): %s\n", entry.NextActionStatus, entry.NextAction)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func projectJournalNextActionCmd() *cobra.Command {
	var status string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "next-action ENTRY_ID",
		Short: "Mark a journal next action open or done",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			entry, err := db.UpdateProjectJournalNextActionStatus(cmd.Context(), args[0], status)
			if err != nil {
				return err
			}
			if entry == nil {
				return fmt.Errorf("journal entry %s not found", args[0])
			}
			if asJSON {
				return printJSON(entry)
			}
			fmt.Printf("Journal next action %s: %s\n", status, entry.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Next action status: open or done")
	_ = cmd.MarkFlagRequired("status")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func projectListDefinitionsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "list", Short: "List canonical Projects", RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		projects, err := db.ListProjectDefinitions(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(projects)
		}
		for _, project := range projects {
			fmt.Printf("%-32s %s\n", project.ID, project.Name)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func projectGetDefinitionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "get PROJECT_ID", Short: "Inspect a Project and its primary Evidence Map", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		project, err := db.GetProjectDefinition(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if project == nil {
			return fmt.Errorf("project %s not found", args[0])
		}
		evidenceMap, err := db.GetActivePrimaryEvidenceChain(cmd.Context(), project.ID)
		if err != nil {
			return err
		}
		result := map[string]any{"project": project, "primary_evidence_map": evidenceMap}
		if asJSON {
			return printJSON(result)
		}
		fmt.Printf("%s  %s\n", project.ID, project.Name)
		if evidenceMap != nil {
			fmt.Printf("evidence map %s  revision %d\n", evidenceMap.ID, evidenceMap.Revision)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

type projectFileConfig struct {
	Path             string
	Project          projectFileMeta
	Resource         string
	Cwd              string
	Env              string
	CondaEnv         string
	TargetEnv        string
	DefaultGPU       *int
	UIEvents         string
	Logs             []string
	Metrics          []string
	Artifacts        []string
	Commands         map[string]projectFileCommand
	ResourceProfiles map[string]projectResourceProfile
	Sync             projectFileSync
	FreezeProfiles   map[string]freezer.Profile
	Warnings         []string
}

type projectFileMeta struct {
	ID               string
	Name             string
	Vault            string
	RunCardIndex     string
	ProposalDir      string
	PromotionDefault string
}

type projectFileCommand struct {
	Name               string
	Command            string
	Kind               string
	GPUIndex           *int
	NoGPU              bool
	TargetEnv          string
	UIEvents           string
	Logs               []string
	Metrics            []string
	Artifacts          []string
	Datasets           []string
	Seeds              []int64
	SplitProtocol      string
	EvaluationProtocol string
	Inputs             []string
	Outputs            []string
	InputBindings      []store.RunInputBinding
	OutputBindings     []store.RunOutputBinding
}

type projectResourceProfile struct {
	Name       string
	Resource   string
	Cwd        string
	Env        string
	CondaEnv   string
	TargetEnv  string
	DefaultGPU *int
	UIEvents   string
	EnvVars    map[string]string
}

type projectFileSync struct {
	Source            string
	Target            string
	Profile           string
	Excludes          []string
	DeleteExtra       bool
	NoDefaultExcludes bool
	TimeoutSec        int
}

func projectInitCmd() *cobra.Command {
	var resourceName, cwd, envStrategy, condaEnv, outputPath string
	var defaultGPU int
	var force, dryRun, noEventsHelper bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a project .aexp.yaml recipe file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectInit(projectInitOptions{
				Resource:       resourceName,
				Cwd:            cwd,
				Env:            envStrategy,
				CondaEnv:       condaEnv,
				OutputPath:     outputPath,
				DefaultGPU:     defaultGPU,
				Force:          force,
				DryRun:         dryRun,
				NoEventsHelper: noEventsHelper,
			})
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Default resource name")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Remote project working directory (default: current directory)")
	cmd.Flags().StringVar(&envStrategy, "env", executor.ProjectEnvAuto, "Runtime env strategy: auto or raw")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Default conda environment")
	cmd.Flags().IntVar(&defaultGPU, "default-gpu", 0, "Default GPU index for formal recipes")
	cmd.Flags().StringVar(&outputPath, "output", "", "Output config path (default: .aexp.yaml)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the config without writing it")
	cmd.Flags().BoolVar(&noEventsHelper, "no-events-helper", false, "Do not create project-local aexp_events.py")
	return cmd
}

type projectInitOptions struct {
	Resource       string
	Cwd            string
	Env            string
	CondaEnv       string
	OutputPath     string
	DefaultGPU     int
	Force          bool
	DryRun         bool
	NoEventsHelper bool
}

func runProjectInit(opts projectInitOptions) error {
	if opts.Env == "" {
		opts.Env = executor.ProjectEnvAuto
	}
	if opts.Env != executor.ProjectEnvAuto && opts.Env != executor.ProjectEnvRaw {
		return fmt.Errorf("--env must be auto or raw")
	}
	localDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}
	outputPath := opts.OutputPath
	if outputPath == "" {
		outputPath = filepath.Join(localDir, ".aexp.yaml")
	} else {
		outputPath = expandPath(outputPath)
		if !filepath.IsAbs(outputPath) {
			outputPath = filepath.Join(localDir, outputPath)
		}
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd = localDir
	}
	guess := guessProjectInit(localDir)
	content := renderProjectInitConfig(projectInitConfig{
		ProjectID:   defaultProjectID(localDir),
		ProjectName: filepath.Base(localDir),
		Resource:    opts.Resource,
		Cwd:         cwd,
		Env:         opts.Env,
		CondaEnv:    opts.CondaEnv,
		DefaultGPU:  opts.DefaultGPU,
		SetupCmd:    guess.SetupCmd,
		TrainCmd:    guess.TrainCmd,
		TrainSafe:   guess.TrainSafe,
		Logs:        guess.Logs,
		Metrics:     guess.Metrics,
		SyncProfile: guess.SyncProfile,
	})
	if opts.DryRun {
		fmt.Printf("target: %s\n", outputPath)
		if !opts.NoEventsHelper {
			fmt.Printf("events helper: %s\n", projectEventsHelperPath(localDir))
		}
		fmt.Println(content)
		printProjectInitNextSteps()
		return nil
	}
	if _, err := os.Stat(outputPath); err == nil && !opts.Force {
		return fmt.Errorf("%s already exists; pass --force to overwrite", outputPath)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("check output file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write project config: %w", err)
	}
	fmt.Printf("Created %s\n", outputPath)
	if !opts.NoEventsHelper {
		if err := writeProjectEventsHelper(localDir); err != nil {
			return err
		}
	}
	printProjectInitNextSteps()
	return nil
}

func projectEventsHelperPath(localDir string) string {
	return filepath.Join(localDir, "aexp_events.py")
}

func writeProjectEventsHelper(localDir string) error {
	helperPath := projectEventsHelperPath(localDir)
	if _, err := os.Stat(helperPath); err == nil {
		fmt.Printf("Kept existing %s\n", helperPath)
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("check events helper: %w", err)
	}
	if err := os.WriteFile(helperPath, []byte(executor.AexpEventsPythonHelper()), 0644); err != nil {
		return fmt.Errorf("write events helper: %w", err)
	}
	fmt.Printf("Created %s\n", helperPath)
	return nil
}

type projectInitConfig struct {
	ProjectID   string
	ProjectName string
	Resource    string
	Cwd         string
	Env         string
	CondaEnv    string
	DefaultGPU  int
	SetupCmd    string
	TrainCmd    string
	TrainSafe   bool
	Logs        []string
	Metrics     []string
	SyncProfile string
}

type projectInitGuess struct {
	SetupCmd    string
	TrainCmd    string
	TrainSafe   bool
	Logs        []string
	Metrics     []string
	SyncProfile string
}

func guessProjectInit(dir string) projectInitGuess {
	guess := projectInitGuess{
		SetupCmd:    "python -m pip install -r requirements.txt",
		TrainCmd:    "",
		TrainSafe:   false,
		Logs:        []string{"logs/**/*.log"},
		Metrics:     []string{"runs/**/*.csv", "results/**/*.json"},
		SyncProfile: "code",
	}
	if fileExists(filepath.Join(dir, "pyproject.toml")) && !fileExists(filepath.Join(dir, "requirements.txt")) {
		guess.SetupCmd = "python -m pip install -e ."
	}
	if fileExists(filepath.Join(dir, "uv.lock")) || fileExists(filepath.Join(dir, "pyproject.toml")) && fileContains(filepath.Join(dir, "pyproject.toml"), "[tool.uv") {
		guess.SetupCmd = "uv sync"
	}
	if candidate := firstExistingGlob(dir, "scripts/train*.sh"); candidate != "" {
		guess.TrainCmd = "bash " + filepath.ToSlash(candidate)
		guess.TrainSafe = true
	} else if candidate := firstExistingGlob(dir, "scripts/train*.py"); candidate != "" {
		guess.TrainCmd = "python " + filepath.ToSlash(candidate)
		guess.TrainSafe = true
	} else if fileExists(filepath.Join(dir, "train.py")) {
		guess.TrainCmd = "python train.py"
		guess.TrainSafe = false
	}
	if guess.TrainCmd != "" && (guess.TrainSafe || !fileExists(filepath.Join(dir, "main.py"))) {
		if candidate := firstExistingGlob(dir, "configs/experiments/*.yaml"); candidate != "" {
			if strings.HasPrefix(guess.TrainCmd, "bash ") {
				guess.TrainCmd += " " + filepath.ToSlash(candidate)
			} else {
				guess.TrainCmd += " --config " + filepath.ToSlash(candidate)
			}
		} else if candidate := firstExistingGlob(dir, "configs/*.yaml"); candidate != "" {
			if strings.HasPrefix(guess.TrainCmd, "bash ") {
				guess.TrainCmd += " " + filepath.ToSlash(candidate)
			} else {
				guess.TrainCmd += " --config " + filepath.ToSlash(candidate)
			}
		}
	}
	if dirExists(filepath.Join(dir, "wandb")) {
		guess.Metrics = append(guess.Metrics, "wandb/**/*.json")
	}
	return guess
}

func appendProjectTrainConfig(b *strings.Builder, cfg projectInitConfig) {
	if cfg.TrainSafe && strings.TrimSpace(cfg.TrainCmd) != "" {
		fmt.Fprintf(b, "train:\n")
		if strings.Contains(cfg.TrainCmd, "\n") {
			fmt.Fprintf(b, "  command: |\n")
			for _, line := range strings.Split(cfg.TrainCmd, "\n") {
				fmt.Fprintf(b, "    %s\n", line)
			}
		} else {
			fmt.Fprintf(b, "  command: %s\n", cfg.TrainCmd)
		}
		fmt.Fprintf(b, "  kind: formal\n")
		return
	}
	fmt.Fprintf(b, "# train:\n")
	fmt.Fprintf(b, "#   command: TODO replace with the real training command\n")
	fmt.Fprintf(b, "#   kind: formal\n")
}

func renderProjectInitConfig(cfg projectInitConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "project:\n")
	fmt.Fprintf(&b, "  id: %s\n", cfg.ProjectID)
	if cfg.ProjectName != "" && cfg.ProjectName != cfg.ProjectID {
		fmt.Fprintf(&b, "  name: %s\n", cfg.ProjectName)
	}
	fmt.Fprintf(&b, "  promotion_default: no_proposal\n\n")
	fmt.Fprintf(&b, "resource: %s\n", cfg.Resource)
	fmt.Fprintf(&b, "cwd: %s\n", cfg.Cwd)
	fmt.Fprintf(&b, "env: %s\n", cfg.Env)
	if cfg.CondaEnv != "" {
		fmt.Fprintf(&b, "conda_env: %s\n", cfg.CondaEnv)
	}
	fmt.Fprintf(&b, "default_gpu: %d\n\n", cfg.DefaultGPU)
	fmt.Fprintf(&b, "# Optional: override only runtime shell details per resource while keeping recipes shared.\n")
	fmt.Fprintf(&b, "# resource_profiles:\n")
	fmt.Fprintf(&b, "#   mu:\n")
	fmt.Fprintf(&b, "#     cwd: /home/murasame/pythonproject/%s\n", filepath.Base(cfg.Cwd))
	fmt.Fprintf(&b, "#     project_env: auto\n")
	fmt.Fprintf(&b, "#     conda_env: my-mu-env\n")
	fmt.Fprintf(&b, "#     target_env: my-training-env\n")
	fmt.Fprintf(&b, "#     default_gpu: 0\n")
	fmt.Fprintf(&b, "#     env_vars:\n")
	fmt.Fprintf(&b, "#       DATA_ROOT: /home/murasame/datasets\n")
	fmt.Fprintf(&b, "#       OUTPUT_ROOT: /home/murasame/outputs\n\n")
	writeYAMLList(&b, "logs", cfg.Logs)
	writeYAMLList(&b, "metrics", cfg.Metrics)
	fmt.Fprintf(&b, "\nsync:\n")
	fmt.Fprintf(&b, "  source: ./\n")
	fmt.Fprintf(&b, "  target: %s\n", cfg.Cwd)
	fmt.Fprintf(&b, "  profile: %s\n\n", firstNonEmpty(cfg.SyncProfile, "code"))
	fmt.Fprintf(&b, "setup:\n")
	fmt.Fprintf(&b, "  command: %s\n", cfg.SetupCmd)
	fmt.Fprintf(&b, "  kind: setup\n\n")
	fmt.Fprintf(&b, "check-results:\n")
	fmt.Fprintf(&b, "  command: ls -lah runs results 2>/dev/null || true\n")
	fmt.Fprintf(&b, "  kind: smoke\n")
	fmt.Fprintf(&b, "  no_gpu: true\n\n")
	fmt.Fprintf(&b, "# Structured UI events belong in training/eval code, not post-hoc CLI calls:\n")
	fmt.Fprintf(&b, "#   from aexp_events import metric, training_epoch, training_done, progress, param, note\n")
	fmt.Fprintf(&b, "#   param(\"model\", \"iTransformer\", trial=trial_id)\n")
	fmt.Fprintf(&b, "#   training_epoch(epoch, total=max_epochs, trial=trial_id, variant=\"sl192_pl96\")\n")
	fmt.Fprintf(&b, "#   metric(\"train/loss\", loss, epoch=epoch, trial=trial_id, variant=\"sl192_pl96\")\n")
	fmt.Fprintf(&b, "#   metric(\"val/observed_mse\", mse, epoch=epoch, trial=trial_id, variant=\"sl192_pl96\", split=\"val\")\n")
	fmt.Fprintf(&b, "#   training_done(epoch=last_epoch, total=max_epochs, best_epoch=best_epoch, early_stopped=early_stopped, trial=trial_id, variant=\"sl192_pl96\")\n")
	appendProjectTrainConfig(&b, cfg)
	return b.String()
}

func writeYAMLList(b *strings.Builder, key string, values []string) {
	fmt.Fprintf(b, "%s:\n", key)
	for _, value := range values {
		fmt.Fprintf(b, "  - %s\n", value)
	}
}

func defaultProjectID(dir string) string {
	name := strings.ToLower(filepath.Base(dir))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "aexp-project"
	}
	return out
}

func projectIDFromConfig(cfg *projectFileConfig) string {
	if cfg != nil {
		if id := strings.TrimSpace(cfg.Project.ID); id != "" {
			return id
		}
		if cfg.Path != "" {
			return defaultProjectID(filepath.Dir(cfg.Path))
		}
	}
	return "aexp-project"
}

func projectNameFromConfig(cfg *projectFileConfig) string {
	if cfg != nil {
		if name := strings.TrimSpace(cfg.Project.Name); name != "" {
			return name
		}
	}
	return projectIDFromConfig(cfg)
}

func printProjectRunCards(cards []store.ProjectRunCard) {
	if len(cards) == 0 {
		fmt.Println("No project run cards found.")
		return
	}
	fmt.Printf("%-14s %-15s %-5s %-9s %-30s %s\n", "UPDATED", "RUN_ID", "LVL", "IMPORTANT", "QUESTION", "VERDICT")
	for _, card := range cards {
		updated := card.UpdatedAt.Format("01-02 15:04")
		if card.UpdatedAt.IsZero() {
			updated = "-"
		}
		important := ""
		if card.Important {
			important = "yes"
		}
		fmt.Printf("%-14s %-15s %-5s %-9s %-30s %s\n",
			updated,
			truncStr(card.RunID, 15),
			firstNonEmpty(card.EvidenceLevel, "C"),
			important,
			truncStr(card.Question, 30),
			truncStr(card.Verdict, 70),
		)
	}
}

func printProjectInitNextSteps() {
	fmt.Println()
	fmt.Println("Next:")
	fmt.Println("  aexp project doctor")
	fmt.Println("  aexp project run check-results --dry-run")
	fmt.Println("  edit .aexp.yaml train recipe, then: aexp project run train --dry-run")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileContains(path string, needle string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), needle)
}

func firstExistingGlob(baseDir string, pattern string) string {
	matches, err := filepath.Glob(filepath.Join(baseDir, filepath.FromSlash(pattern)))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	rel, err := filepath.Rel(baseDir, matches[0])
	if err != nil {
		return matches[0]
	}
	return rel
}

func projectRunCmd() *cobra.Command {
	var configPath, resourceName, cwd, name, kind, projectEnv, condaEnv, targetEnv, uiEventsPath, forceReason, preemptRunID string
	var gpuIndex int
	var noGPU, force, dryRun, refreshEnv, allowEphemeralPaths, preemptSave, allowDirtyGit, recordGitDiff bool
	var launchTimeoutSec int

	cmd := &cobra.Command{
		Use:   "run [name]",
		Short: "Submit a configured project command from .aexp.yaml",
		Long: `Submit a command declared in .aexp.yaml.

Example:
  train:
    command: python train.py

Then run:
  aexp project run train`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandName := "train"
			if len(args) > 0 {
				commandName = args[0]
			}
			cfg, err := loadProjectFileConfig(configPath)
			if err != nil {
				return err
			}
			entry, ok := cfg.Commands[commandName]
			if !ok || strings.TrimSpace(entry.Command) == "" {
				return fmt.Errorf("project command %q not found in %s", commandName, cfg.Path)
			}

			resourceFlagChanged := cmd.Flags().Changed("resource")
			cwdFlagChanged := cmd.Flags().Changed("cwd")
			projectEnvFlagChanged := cmd.Flags().Changed("project-env")
			condaEnvFlagChanged := cmd.Flags().Changed("conda-env")

			requestedResource := resourceName
			if !resourceFlagChanged && requestedResource == "" {
				requestedResource = cfg.Resource
			}
			resourceProfile, hasResourceProfile := selectProjectResourceProfile(cfg, requestedResource)
			if resourceName == "" {
				resourceName = cfg.Resource
			}
			if hasResourceProfile {
				if resourceProfile.Resource != "" {
					resourceName = resourceProfile.Resource
				} else if resourceName == "" {
					resourceName = resourceProfile.Name
				}
			}
			if resourceName == "" {
				return fmt.Errorf("resource is required: set resource: in %s or pass --resource", cfg.Path)
			}
			if !cwdFlagChanged && hasResourceProfile && resourceProfile.Cwd != "" {
				cwd = resourceProfile.Cwd
			}
			if cwd == "" {
				cwd = cfg.Cwd
			}
			if cwd == "" {
				cwd = "."
			}
			if kind == "" {
				kind = entry.Kind
			}
			if kind == "" {
				kind = store.RunKindFormal
			}
			if !projectEnvFlagChanged && hasResourceProfile && resourceProfile.Env != "" {
				projectEnv = resourceProfile.Env
			}
			if projectEnv == "" {
				projectEnv = cfg.Env
			}
			if projectEnv == "" {
				projectEnv = executor.ProjectEnvAuto
			}
			if !condaEnvFlagChanged && hasResourceProfile && resourceProfile.CondaEnv != "" {
				condaEnv = resourceProfile.CondaEnv
			}
			if condaEnv == "" {
				condaEnv = cfg.CondaEnv
			}
			effectiveTargetEnv := cfg.TargetEnv
			if hasResourceProfile && resourceProfile.TargetEnv != "" {
				effectiveTargetEnv = resourceProfile.TargetEnv
			}
			if entry.TargetEnv != "" {
				effectiveTargetEnv = entry.TargetEnv
			}
			if cmd.Flags().Changed("target-env") {
				effectiveTargetEnv = targetEnv
			}
			if name == "" {
				name = entry.Name
			}
			if name == "" {
				name = commandName
			}
			effectiveGPU := store.GPUIndexAll
			if cfg.DefaultGPU != nil {
				effectiveGPU = *cfg.DefaultGPU
			}
			if hasResourceProfile && resourceProfile.DefaultGPU != nil {
				effectiveGPU = *resourceProfile.DefaultGPU
			}
			if entry.GPUIndex != nil {
				effectiveGPU = *entry.GPUIndex
			}
			if cmd.Flags().Changed("gpu-index") {
				effectiveGPU = gpuIndex
			}
			if noGPU || entry.NoGPU || (kind == store.RunKindSetup && entry.GPUIndex == nil && !cmd.Flags().Changed("gpu-index")) {
				effectiveGPU = store.GPUIndexNone
			}

			logPaths := mergeProjectLists(cfg.Logs, entry.Logs)
			metricPaths := mergeProjectLists(cfg.Metrics, entry.Metrics)
			artifactPaths := mergeProjectLists(cfg.Artifacts, entry.Artifacts)
			managedInputs := append([]store.RunInputBinding(nil), entry.InputBindings...)
			for _, spec := range entry.Inputs {
				binding, parseErr := parseRunInputSpec(spec)
				if parseErr != nil {
					return parseErr
				}
				managedInputs = append(managedInputs, binding)
			}
			managedOutputs := append([]store.RunOutputBinding(nil), entry.OutputBindings...)
			for _, spec := range entry.Outputs {
				binding, parseErr := parseRunOutputSpec(spec)
				if parseErr != nil {
					return parseErr
				}
				managedOutputs = append(managedOutputs, binding)
			}
			datasetInputs, err := resolveRunDatasetInputs(cmd.Context(), entry.Datasets)
			if err != nil {
				return err
			}
			configHash, err := fileSHA256(cfg.Path)
			if err != nil {
				return fmt.Errorf("hash project config: %w", err)
			}
			if uiEventsPath == "" {
				uiEventsPath = entry.UIEvents
			}
			if uiEventsPath == "" && hasResourceProfile {
				uiEventsPath = resourceProfile.UIEvents
			}
			if uiEventsPath == "" {
				uiEventsPath = cfg.UIEvents
			}
			envVars := map[string]string(nil)
			if hasResourceProfile {
				envVars = copyStringMap(resourceProfile.EnvVars)
				if envVars == nil {
					envVars = map[string]string{}
				}
				envVars["AEXP_RESOURCE_PROFILE"] = resourceProfile.Name
			}
			submitReq := executor.SubmitRequest{
				ResourceID:          resourceName,
				ProjectID:           cfg.Project.ID,
				RecipeName:          commandName,
				Name:                name,
				Kind:                kind,
				GPUIndex:            effectiveGPU,
				Force:               force,
				ForceReason:         forceReason,
				PreemptRunID:        preemptRunID,
				PreemptSave:         preemptSave,
				Cwd:                 cwd,
				CondaEnv:            condaEnv,
				ProjectEnv:          projectEnv,
				TargetEnv:           effectiveTargetEnv,
				LogPaths:            logPaths,
				MetricPaths:         metricPaths,
				ArtifactPaths:       artifactPaths,
				UIEventsPath:        uiEventsPath,
				EnvVars:             envVars,
				Program:             "bash",
				Args:                []string{"-lc", entry.Command},
				RefreshProjectEnv:   refreshEnv,
				AllowEphemeralPaths: allowEphemeralPaths,
				GitSourceDir:        filepath.Dir(cfg.Path),
				AllowDirtyGit:       allowDirtyGit,
				RecordGitDiff:       recordGitDiff,
				ProjectConfigSHA256: configHash,
				Datasets:            datasetInputs,
				Seeds:               append([]int64(nil), entry.Seeds...),
				SplitProtocol:       entry.SplitProtocol,
				EvaluationProtocol:  entry.EvaluationProtocol,
				Inputs:              managedInputs,
				Outputs:             managedOutputs,
			}
			if dryRun {
				printProjectConfigWarnings(cfg)
				printProjectRunPlan(cfg.Path, commandName, resourceName, submitReq)
				return nil
			}
			return submitConfiguredRun(cmd.Context(), resourceName, submitReq, launchTimeoutSec)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path (default: nearest .aexp.yaml)")
	cmd.Flags().StringVar(&resourceName, "resource", "", "Override resource name")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Override working directory")
	cmd.Flags().StringVar(&name, "name", "", "Override run name")
	cmd.Flags().StringVar(&kind, "kind", "", "Override run kind")
	cmd.Flags().IntVar(&gpuIndex, "gpu-index", store.GPUIndexAll, "Override GPU index (-1 for all)")
	cmd.Flags().BoolVar(&noGPU, "no-gpu", false, "Do not reserve GPUs or set CUDA_VISIBLE_DEVICES")
	cmd.Flags().BoolVar(&force, "force", false, "Skip GPU slot lock")
	cmd.Flags().StringVar(&forceReason, "force-reason", "", "Required with --force or --preempt-run; records why GPU safety was overridden")
	cmd.Flags().StringVar(&preemptRunID, "preempt-run", "", "Cancel this active run before submitting the new run")
	cmd.Flags().BoolVar(&preemptSave, "preempt-save", true, "Record that the preempted run should be preserved for evidence review")
	cmd.Flags().StringVar(&projectEnv, "project-env", "", "Override runtime env strategy: auto or raw")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Override conda environment")
	cmd.Flags().StringVar(&targetEnv, "target-env", "", "Override intended target environment recorded on the run")
	cmd.Flags().StringVar(&uiEventsPath, "ui-events", "", "Override structured UI event JSONL path; set off to disable")
	cmd.Flags().BoolVar(&refreshEnv, "refresh-env", false, "Ignore cached project profile and re-detect the environment")
	cmd.Flags().BoolVar(&allowEphemeralPaths, "allow-ephemeral-paths", false, "Allow cwd/root_dir that look like temporary mounts; use only for disposable smoke/setup runs")
	cmd.Flags().BoolVar(&allowDirtyGit, "allow-dirty-git", false, "Allow a formal/ablation run from a dirty Git worktree")
	cmd.Flags().BoolVar(&recordGitDiff, "record-git-diff", false, "When allowing dirty Git, save a local patch under ~/.aexp/git-diffs")
	cmd.Flags().BoolVar(&allowDirtyGit, "allow-dirty", false, "Alias for --allow-dirty-git")
	cmd.Flags().BoolVar(&recordGitDiff, "record-diff", false, "Alias for --record-git-diff")
	_ = cmd.Flags().MarkHidden("allow-dirty")
	_ = cmd.Flags().MarkHidden("record-diff")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the resolved submit command without launching")
	cmd.Flags().IntVar(&launchTimeoutSec, "launch-timeout", 60, "Timeout in seconds for remote launch after the run record is created")
	return cmd
}

func projectSyncCmd() *cobra.Command {
	var configPath, resourceName, source, target, profile string
	var dryRun, deleteExtra, noDefaultExcludes bool
	var excludes []string
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Push files using sync settings from .aexp.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadProjectFileConfig(configPath)
			if err != nil {
				return err
			}
			if resourceName == "" {
				resourceName = cfg.Resource
			}
			if source == "" {
				source = cfg.Sync.Source
			}
			if source == "" {
				source = "."
			}
			if target == "" {
				target = cfg.Sync.Target
			}
			if target == "" {
				target = cfg.Cwd
			}
			if resourceName == "" || target == "" {
				return fmt.Errorf("project sync needs resource and target: set resource/cwd or sync.target in %s", cfg.Path)
			}
			if profile == "" {
				profile = cfg.Sync.Profile
			}
			if profile == "" {
				profile = "code"
			}
			if !cmd.Flags().Changed("delete") {
				deleteExtra = cfg.Sync.DeleteExtra
			}
			if !cmd.Flags().Changed("no-default-excludes") {
				noDefaultExcludes = cfg.Sync.NoDefaultExcludes
			}
			if !cmd.Flags().Changed("timeout") {
				timeoutSec = cfg.Sync.TimeoutSec
			}
			excludes = append(cfg.Sync.Excludes, excludes...)
			return runSyncPushFromProject(cmd.Context(), resourceName, source, target, profile, excludes, dryRun, deleteExtra, noDefaultExcludes, timeoutSec)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path (default: nearest .aexp.yaml)")
	cmd.Flags().StringVar(&resourceName, "resource", "", "Override resource name")
	cmd.Flags().StringVar(&source, "source", "", "Override local source")
	cmd.Flags().StringVar(&target, "target", "", "Override remote target")
	cmd.Flags().StringVar(&profile, "profile", "", "Override exclude profile: code, code-data, all")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the rsync command without running it")
	cmd.Flags().BoolVar(&deleteExtra, "delete", false, "Delete files on target that no longer exist on source")
	cmd.Flags().BoolVar(&noDefaultExcludes, "no-default-excludes", false, "Disable profile excludes and .aexpignore")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "Extra exclude pattern, repeatable")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 0, "Timeout in seconds (0 = no timeout)")
	return cmd
}

func projectCardCmd() *cobra.Command {
	var configPath, question, verdict, level, supports, weakens, nextAction, proposalReason string
	var keyMetrics, artifactPaths, relatedRuns []string
	var important, promote, asJSON, reassignProject bool

	cmd := &cobra.Command{
		Use:   "card [run_id]",
		Short: "Create or update a project-level experiment card for a run",
		Long: `Create a short project-level card that explains why a run matters.

The card is scoped by the nearest .aexp.yaml project.id. It is intentionally
short: experiment agents should write facts and verdicts here, while note agents
can later read project digest/runs without scanning raw logs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadProjectFileConfig(configPath)
			if err != nil {
				return err
			}
			projectID := projectIDFromConfig(cfg)
			db := openDB()
			defer db.Close()
			card, err := db.GetProjectRunCard(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if card == nil {
				card = &store.ProjectRunCard{
					ID:            "card_" + strings.TrimPrefix(args[0], "run_"),
					ProjectID:     projectID,
					ProjectName:   projectNameFromConfig(cfg),
					RunID:         args[0],
					EvidenceLevel: "C",
				}
			} else if reassignProject {
				card, err = db.ReassignProjectRunCard(cmd.Context(), card.RunID, projectID, projectNameFromConfig(cfg), card.UpdatedAt)
				if err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("question") {
				card.Question = question
			}
			if cmd.Flags().Changed("verdict") {
				card.Verdict = verdict
			}
			if cmd.Flags().Changed("level") {
				card.EvidenceLevel = strings.ToUpper(strings.TrimSpace(level))
			}
			if len(keyMetrics) > 0 {
				card.KeyMetrics = strings.Join(keyMetrics, "\n")
			}
			if len(artifactPaths) > 0 {
				card.ArtifactPaths = strings.Join(artifactPaths, "\n")
			}
			if cmd.Flags().Changed("supports") {
				card.SupportsClaim = supports
			}
			if cmd.Flags().Changed("weakens") {
				card.WeakensClaim = weakens
			}
			if cmd.Flags().Changed("next-action") {
				card.NextAction = nextAction
			}
			if cmd.Flags().Changed("important") {
				card.Important = important
			}
			if cmd.Flags().Changed("promote") {
				card.ShouldPromote = promote
			}
			if cmd.Flags().Changed("proposal-reason") {
				card.ProposalReason = proposalReason
			}
			if len(relatedRuns) > 0 {
				card.RelatedRuns = strings.Join(relatedRuns, "\n")
			}
			if card.EvidenceLevel == "" {
				card.EvidenceLevel = "C"
			}
			if err := db.SaveProjectRunCard(cmd.Context(), card); err != nil {
				return err
			}
			if asJSON {
				return printJSON(card)
			}
			fmt.Printf("Saved project card for %s in %s\n", card.RunID, card.ProjectID)
			if card.Important {
				fmt.Println("important: true")
			}
			if card.ShouldPromote {
				fmt.Println("promote: true")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path (default: nearest .aexp.yaml)")
	cmd.Flags().StringVar(&question, "question", "", "What this run was meant to answer")
	cmd.Flags().StringVar(&verdict, "verdict", "", "One-sentence conclusion")
	cmd.Flags().StringVar(&level, "level", "C", "Evidence level: A, B, or C")
	cmd.Flags().StringSliceVar(&keyMetrics, "metric", nil, "Key metric line, repeatable")
	cmd.Flags().StringSliceVar(&artifactPaths, "artifact", nil, "Artifact path, repeatable")
	cmd.Flags().StringVar(&supports, "supports", "", "Claim this run supports")
	cmd.Flags().StringVar(&weakens, "weakens", "", "Claim this run weakens")
	cmd.Flags().StringVar(&nextAction, "next-action", "", "Recommended next action")
	cmd.Flags().BoolVar(&important, "important", false, "Mark this run as important for project review")
	cmd.Flags().BoolVar(&promote, "promote", false, "Mark this card as worth promoting to notes/proposal")
	cmd.Flags().StringVar(&proposalReason, "proposal-reason", "", "Why this deserves promotion")
	cmd.Flags().StringSliceVar(&relatedRuns, "related-run", nil, "Related run id, repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&reassignProject, "reassign-project", false, "Explicitly move an existing card to the project in this config")
	return cmd
}

func projectRunsCmd() *cobra.Command {
	var configPath string
	var importantOnly, asJSON bool
	var limit int

	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List project-level experiment cards",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadProjectFileConfig(configPath)
			if err != nil {
				return err
			}
			db := openDB()
			defer db.Close()
			cards, err := db.ListProjectRunCards(cmd.Context(), store.ProjectRunCardFilter{
				ProjectID:     projectIDFromConfig(cfg),
				ImportantOnly: importantOnly,
				Limit:         limit,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cards)
			}
			printProjectRunCards(cards)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path (default: nearest .aexp.yaml)")
	cmd.Flags().BoolVar(&importantOnly, "important", false, "Show only important cards")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of cards")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func projectDigestCmd() *cobra.Command {
	var configPath string
	var importantOnly, asJSON bool
	var limit int

	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Print a note-agent friendly project experiment digest",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadProjectFileConfig(configPath)
			if err != nil {
				return err
			}
			projectID := projectIDFromConfig(cfg)
			db := openDB()
			defer db.Close()
			cards, err := db.ListProjectRunCards(cmd.Context(), store.ProjectRunCardFilter{
				ProjectID:     projectID,
				ImportantOnly: importantOnly,
				Limit:         limit,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]interface{}{
					"project_id":   projectID,
					"project_name": projectNameFromConfig(cfg),
					"cards":        cards,
				})
			}
			fmt.Printf("# aexp project digest: %s\n\n", projectID)
			if len(cards) == 0 {
				fmt.Println("No project run cards yet.")
				return nil
			}
			for _, card := range cards {
				fmt.Printf("## %s  level=%s", card.RunID, firstNonEmpty(card.EvidenceLevel, "C"))
				if card.Important {
					fmt.Print("  important")
				}
				if card.ShouldPromote {
					fmt.Print("  promote")
				}
				fmt.Println()
				if card.Question != "" {
					fmt.Printf("- question: %s\n", card.Question)
				}
				if card.Verdict != "" {
					fmt.Printf("- verdict: %s\n", card.Verdict)
				}
				if card.KeyMetrics != "" {
					fmt.Printf("- metrics: %s\n", strings.ReplaceAll(card.KeyMetrics, "\n", "; "))
				}
				if card.NextAction != "" {
					fmt.Printf("- next: %s\n", card.NextAction)
				}
				if card.RelatedRuns != "" {
					fmt.Printf("- related: %s\n", strings.ReplaceAll(card.RelatedRuns, "\n", ", "))
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path (default: nearest .aexp.yaml)")
	cmd.Flags().BoolVar(&importantOnly, "important", false, "Show only important cards")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of cards")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

type evidenceChainAgentSnapshot struct {
	ChainID      string                          `json:"chain_id"`
	Title        string                          `json:"title"`
	Description  string                          `json:"description,omitempty"`
	RoutingHints store.EvidenceGraphRoutingHints `json:"routing_hints"`
	ProjectID    string                          `json:"project_id,omitempty"`
	Role         string                          `json:"role"`
	Status       string                          `json:"status"`
	Revision     int64                           `json:"revision"`
	GraphHash    string                          `json:"graph_hash"`
	Intro        string                          `json:"intro"`
	UpdatedAt    time.Time                       `json:"updated_at"`
	Nodes        []evidenceChainAgentNode        `json:"nodes"`
	Edges        []evidenceChainAgentEdge        `json:"edges"`
}

type evidenceChainAgentNode struct {
	NodeID        string                       `json:"node_id"`
	Type          string                       `json:"type"`
	Title         string                       `json:"title"`
	Body          string                       `json:"body,omitempty"`
	RunID         string                       `json:"run_id,omitempty"`
	ProjectCardID string                       `json:"project_card_id,omitempty"`
	ShortIntro    string                       `json:"short_intro"`
	Run           *evidenceChainRunSummary     `json:"run,omitempty"`
	ProjectCard   *evidenceChainProjectSummary `json:"project_card,omitempty"`
	Marks         []evidenceChainMarkSummary   `json:"marks,omitempty"`
}

type evidenceChainAgentEdge struct {
	EdgeID     string `json:"edge_id"`
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`
	Type       string `json:"type"`
	Label      string `json:"label,omitempty"`
	Rationale  string `json:"rationale,omitempty"`
}

type evidenceChainRunSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Command    string `json:"command,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	GitBranch  string `json:"git_branch,omitempty"`
	GitCommit  string `json:"git_commit,omitempty"`
	GitDirty   bool   `json:"git_dirty,omitempty"`
}

type evidenceChainProjectSummary struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id,omitempty"`
	ProjectName   string `json:"project_name,omitempty"`
	Question      string `json:"question,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	EvidenceLevel string `json:"evidence_level,omitempty"`
	KeyMetrics    string `json:"key_metrics,omitempty"`
	NextAction    string `json:"next_action,omitempty"`
	SupportsClaim string `json:"supports_claim,omitempty"`
	WeakensClaim  string `json:"weakens_claim,omitempty"`
}

type evidenceChainMarkSummary struct {
	ID        string `json:"id"`
	Actor     string `json:"actor,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Title     string `json:"title,omitempty"`
	Statement string `json:"statement,omitempty"`
}

type evidenceProposalCLIView struct {
	store.EvidenceProposal
	TargetMap *store.EvidenceChain `json:"target_map,omitempty"`
}

func buildEvidenceProposalCLIView(ctx context.Context, db *store.SQLite, proposal *store.EvidenceProposal) (*evidenceProposalCLIView, error) {
	if proposal == nil {
		return nil, nil
	}
	view := &evidenceProposalCLIView{EvidenceProposal: *proposal}
	if proposal.TargetChainID == "" {
		return view, nil
	}
	target, err := db.GetEvidenceChain(ctx, proposal.TargetChainID)
	if err != nil {
		return nil, err
	}
	view.TargetMap = target
	return view, nil
}

func evidenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evidence",
		Short: "Inspect a Project Evidence Map and submit reviewable proposals",
		Long: `Evidence Maps contain Project research reasoning. Agents submit
revision-aware proposals for review; they do not directly mutate the accepted
graph. A Project may have one primary graph and multiple topic graphs. Agents
list the Project graphs first, choose a topic graph only when its purpose and
routing hints clearly match, or create a new topic. Missing targets remain
unrouted drafts; the primary graph is never a fallback.`,
	}
	cmd.AddCommand(evidenceListCmd())
	cmd.AddCommand(evidenceCreateCmd())
	cmd.AddCommand(evidenceShowCmd())
	cmd.AddCommand(evidenceAddNodeCmd())
	cmd.AddCommand(evidenceAddEdgeCmd())
	cmd.AddCommand(evidenceProposeCmd())
	cmd.AddCommand(evidenceProposalSubmitCmd())
	cmd.AddCommand(evidenceProposalListCmd())
	cmd.AddCommand(evidenceProposalGetCmd())
	cmd.AddCommand(evidenceProposalPlanCmd())
	cmd.AddCommand(evidenceProposalReviewCmd())
	cmd.AddCommand(evidenceProposalRerouteCmd())
	cmd.AddCommand(evidenceMigrateOrphansCmd())
	cmd.AddCommand(evidencePromotionPlanCmd())
	cmd.AddCommand(evidencePromotionCreateCmd())
	for _, command := range cmd.Commands() {
		switch command.Name() {
		case "create", "add-node", "add-edge", "list":
			command.Hidden = true
		}
	}
	return cmd
}

func evidenceListCmd() *cobra.Command {
	var query, projectID, role, status string
	var limit, offset int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Evidence Chains",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			chains, err := db.ListEvidenceChains(cmd.Context(), store.EvidenceChainFilter{
				Query: query, ProjectID: projectID, Role: role, Status: status, Limit: limit, Offset: offset,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(chains)
			}
			if len(chains) == 0 {
				fmt.Println("No Evidence Chains yet.")
				return nil
			}
			for _, chain := range chains {
				fmt.Printf("%s\t%s", chain.ID, chain.Title)
				if chain.Description != "" {
					fmt.Printf("\t%s", evidencePreview(chain.Description, 90))
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "Search chain id, title, or description")
	cmd.Flags().StringVar(&projectID, "project", "", "Only graphs belonging to this Project")
	cmd.Flags().StringVar(&role, "role", "", "Only graphs with this role: primary, secondary, or archive")
	cmd.Flags().StringVar(&status, "status", "", "Only graphs with this status: active or archived")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum chains to list")
	cmd.Flags().IntVar(&offset, "offset", 0, "Number of graphs to skip")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceCreateCmd() *cobra.Command {
	var description, purpose, projectID string
	var recipes, keywords []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Create an Evidence Chain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.TrimSpace(args[0])
			if title == "" {
				return fmt.Errorf("title is required")
			}
			db := openDB()
			defer db.Close()
			projectID = strings.TrimSpace(projectID)
			if projectID == "" {
				return fmt.Errorf("--project is required; active Evidence Maps cannot be orphaned")
			}
			project, err := db.GetProjectDefinition(cmd.Context(), projectID)
			if err != nil {
				return err
			}
			if project == nil {
				return fmt.Errorf("project %q is not registered", projectID)
			}
			chain := &store.EvidenceChain{
				ID:          genID("chain_"),
				Title:       title,
				Description: firstNonEmpty(purpose, description),
				RoutingHints: store.EvidenceGraphRoutingHints{
					Recipes:  recipes,
					Keywords: keywords,
				},
				ProjectID: projectID,
				Role:      "secondary",
				Status:    "active",
			}
			if err := db.CreateEvidenceChain(cmd.Context(), chain); err != nil {
				return err
			}
			if asJSON {
				return printJSON(chain)
			}
			fmt.Printf("Created Evidence Chain %s: %s\n", chain.ID, chain.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "Short description")
	cmd.Flags().StringVar(&purpose, "purpose", "", "What research question or evidence belongs in this graph")
	cmd.Flags().StringVar(&projectID, "project", "", "Canonical Project id (required)")
	cmd.Flags().StringSliceVar(&recipes, "recipe", nil, "Recipe routing hint (repeatable)")
	cmd.Flags().StringSliceVar(&keywords, "keyword", nil, "Keyword routing hint (repeatable)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [chain_id]",
		Short: "Show an agent-readable Evidence Chain snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			snapshot, err := buildEvidenceChainAgentSnapshot(cmd.Context(), db, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(snapshot)
			}
			printEvidenceChainSnapshot(snapshot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceAddNodeCmd() *cobra.Command {
	var nodeID, nodeType, title, body, runID, projectCardID string
	var width, height float64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add-node [chain_id]",
		Short: "Add one card to an Evidence Chain",
		Long: `Add one card to an Evidence Chain.

This only creates a non-overlapping card. It does not attempt to arrange the
board; humans should move and resize cards in the UI.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeType = strings.TrimSpace(nodeType)
			if nodeType == "" {
				nodeType = store.EvidenceNodeNote
			}
			if !validEvidenceNodeTypeForCLI(nodeType) {
				return fmt.Errorf("invalid node type %q", nodeType)
			}
			db := openDB()
			defer db.Close()
			chain, graph, err := loadEvidenceChainGraphForEdit(cmd.Context(), db, args[0])
			if err != nil {
				return err
			}
			if nodeID == "" {
				nodeID = genID("node_")
			}
			if evidenceNodeExists(graph.Nodes, nodeID) {
				return fmt.Errorf("node %q already exists", nodeID)
			}
			runID = strings.TrimSpace(runID)
			projectCardID = strings.TrimSpace(projectCardID)
			var run *store.Run
			var card *store.ProjectRunCard
			if nodeType == store.EvidenceNodeRun {
				if runID == "" {
					return fmt.Errorf("run node requires --run-id")
				}
				run, err = db.GetRun(cmd.Context(), runID)
				if err != nil {
					return err
				}
				if run == nil {
					return fmt.Errorf("run %q does not exist", runID)
				}
				card, _ = db.GetProjectRunCard(cmd.Context(), runID)
				if projectCardID == "" && card != nil {
					projectCardID = card.ID
				}
			}
			x, y := nextEvidenceNodePosition(graph.Nodes)
			if width <= 0 {
				width = 280
			}
			if height <= 0 {
				height = 150
			}
			node := store.EvidenceChainNode{
				ID:            nodeID,
				ChainID:       args[0],
				Type:          nodeType,
				Title:         firstNonEmpty(title, evidenceDefaultNodeTitle(nodeType, run, card)),
				Body:          strings.TrimSpace(body),
				RunID:         runID,
				ProjectCardID: projectCardID,
				X:             x,
				Y:             y,
				Width:         width,
				Height:        height,
				DataJSON:      "{}",
			}
			graph.Nodes = append(graph.Nodes, node)
			if _, err := db.SaveEvidenceChainGraphCAS(cmd.Context(), args[0], *graph, store.EvidenceGraphSaveOptions{
				ExpectedRevision: chain.Revision,
				Actor:            "cli",
				SourceKind:       "add_node",
				SourceID:         node.ID,
			}); err != nil {
				return err
			}
			if saved, ok := evidenceNodeByID(ctxGraph(cmd.Context(), db, args[0]), node.ID); ok {
				node = saved
			}
			if asJSON {
				return printJSON(node)
			}
			fmt.Printf("Added node %s (%s) to %s\n", node.ID, node.Type, args[0])
			if node.RunID != "" {
				fmt.Printf("run_id: %s\n", node.RunID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&nodeID, "id", "", "Node id (default: generated)")
	cmd.Flags().StringVar(&nodeType, "type", store.EvidenceNodeNote, "Node type: run, hypothesis, experiment, plan, conclusion, note")
	cmd.Flags().StringVar(&title, "title", "", "Card title")
	cmd.Flags().StringVar(&body, "body", "", "Card body")
	cmd.Flags().StringVar(&runID, "run-id", "", "Run id for run nodes")
	cmd.Flags().StringVar(&projectCardID, "project-card-id", "", "Project card id for run nodes")
	cmd.Flags().Float64Var(&width, "width", 0, "Initial card width")
	cmd.Flags().Float64Var(&height, "height", 0, "Initial card height")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceAddEdgeCmd() *cobra.Command {
	var edgeID, fromNodeID, toNodeID, edgeType, label, rationale string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add-edge [chain_id]",
		Short: "Add one typed relationship edge to an Evidence Chain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromNodeID = strings.TrimSpace(fromNodeID)
			toNodeID = strings.TrimSpace(toNodeID)
			if fromNodeID == "" || toNodeID == "" {
				return fmt.Errorf("--from and --to are required")
			}
			edgeType = strings.TrimSpace(edgeType)
			if edgeType == "" {
				edgeType = store.EvidenceEdgeNextStep
			}
			if !validEvidenceEdgeTypeForCLI(edgeType) {
				return fmt.Errorf("invalid edge type %q", edgeType)
			}
			if edgeType == store.EvidenceEdgeCustom && strings.TrimSpace(label) == "" {
				return fmt.Errorf("custom edge requires --label")
			}
			db := openDB()
			defer db.Close()
			chain, graph, err := loadEvidenceChainGraphForEdit(cmd.Context(), db, args[0])
			if err != nil {
				return err
			}
			if !evidenceNodeExists(graph.Nodes, fromNodeID) {
				return fmt.Errorf("source node %q does not exist", fromNodeID)
			}
			if !evidenceNodeExists(graph.Nodes, toNodeID) {
				return fmt.Errorf("target node %q does not exist", toNodeID)
			}
			if edgeID == "" {
				edgeID = genID("edge_")
			}
			for _, edge := range graph.Edges {
				if edge.ID == edgeID {
					return fmt.Errorf("edge %q already exists", edgeID)
				}
			}
			edge := store.EvidenceChainEdge{
				ID:           edgeID,
				ChainID:      args[0],
				SourceNodeID: fromNodeID,
				TargetNodeID: toNodeID,
				Type:         edgeType,
				Label:        firstNonEmpty(label, defaultEvidenceEdgeLabel(edgeType)),
				Rationale:    strings.TrimSpace(rationale),
				DataJSON:     "{}",
			}
			graph.Edges = append(graph.Edges, edge)
			if _, err := db.SaveEvidenceChainGraphCAS(cmd.Context(), args[0], *graph, store.EvidenceGraphSaveOptions{
				ExpectedRevision: chain.Revision,
				Actor:            "cli",
				SourceKind:       "add_edge",
				SourceID:         edge.ID,
			}); err != nil {
				return err
			}
			if saved, ok := evidenceEdgeByID(ctxGraph(cmd.Context(), db, args[0]), edge.ID); ok {
				edge = saved
			}
			if asJSON {
				return printJSON(edge)
			}
			fmt.Printf("Added edge %s: %s --%s--> %s\n", edge.ID, edge.SourceNodeID, edge.Type, edge.TargetNodeID)
			return nil
		},
	}
	cmd.Flags().StringVar(&edgeID, "id", "", "Edge id (default: generated)")
	cmd.Flags().StringVar(&fromNodeID, "from", "", "Source node id")
	cmd.Flags().StringVar(&toNodeID, "to", "", "Target node id")
	cmd.Flags().StringVar(&edgeType, "type", store.EvidenceEdgeNextStep, "Edge type: supports, does_not_prove, next_step, custom")
	cmd.Flags().StringVar(&label, "label", "", "Edge label")
	cmd.Flags().StringVar(&rationale, "rationale", "", "Why this relation should exist")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposeCmd() *cobra.Command {
	var chainID, patchJSON, reason, routingReason string
	var baseRevision int64
	var noGraphImpact, asJSON bool
	cmd := &cobra.Command{
		Use:   "propose [run_id]",
		Short: "Submit a reviewable Research Graph patch or no-impact reason",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("run %q not found", args[0])
			}
			card, err := db.GetProjectRunCard(cmd.Context(), run.ID)
			if err != nil {
				return err
			}
			if card == nil {
				card = &store.ProjectRunCard{
					ID:            "card_" + strings.TrimPrefix(run.ID, "run_"),
					ProjectID:     run.ProjectID,
					RunID:         run.ID,
					EvidenceLevel: "C",
				}
			}
			card.NoGraphImpact = noGraphImpact
			card.GraphImpactReason = strings.TrimSpace(reason)
			var patch *store.EvidenceGraphPatch
			if !noGraphImpact {
				if strings.TrimSpace(patchJSON) == "" {
					return fmt.Errorf("--patch-json is required unless --no-graph-impact is set")
				}
				var decoded store.EvidenceGraphPatch
				if err := json.Unmarshal([]byte(patchJSON), &decoded); err != nil {
					return fmt.Errorf("decode --patch-json: %w", err)
				}
				if strings.TrimSpace(chainID) != "" {
					decoded.ChainID = strings.TrimSpace(chainID)
				}
				if strings.TrimSpace(routingReason) != "" {
					decoded.RoutingReason = strings.TrimSpace(routingReason)
				}
				if decoded.ChainID != "" && baseRevision < 0 {
					chain, err := db.GetEvidenceChain(cmd.Context(), decoded.ChainID)
					if err != nil {
						return err
					}
					if chain == nil {
						return fmt.Errorf("evidence chain %q not found", decoded.ChainID)
					}
					card.BaseGraphRevision = chain.Revision
				} else if decoded.ChainID != "" {
					card.BaseGraphRevision = baseRevision
				}
				patch = &decoded
			}
			saved, err := db.SubmitEvidenceGraphProposal(cmd.Context(), card, patch)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(saved)
			}
			if saved.NoGraphImpact {
				fmt.Printf("Recorded no graph impact for %s: %s\n", saved.RunID, saved.GraphImpactReason)
				return nil
			}
			fmt.Printf("Submitted graph proposal %s for %s at revision %d\n", saved.ProposalHash, saved.RunID, saved.BaseGraphRevision)
			return nil
		},
	}
	cmd.Flags().StringVar(&chainID, "chain", "", "Explicit target graph (default: the Run project's primary Evidence Map)")
	cmd.Flags().StringVar(&patchJSON, "patch-json", "", "Additive patch JSON with nodes and edges")
	cmd.Flags().Int64Var(&baseRevision, "base-revision", -1, "Expected revision for an explicit --chain (project primary defaults to current)")
	cmd.Flags().BoolVar(&noGraphImpact, "no-graph-impact", false, "Record that this run does not change the research graph")
	cmd.Flags().StringVar(&reason, "reason", "", "Required reason for --no-graph-impact")
	cmd.Flags().StringVar(&routingReason, "routing-reason", "", "Why this Run belongs in the explicitly selected topic graph")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposalSubmitCmd() *cobra.Command {
	var targetMapID, actor, summary, routingReason, patchJSON string
	var sourceRunIDs, sourceSnapshotIDs []string
	var projectLevelImpact, asJSON bool
	cmd := &cobra.Command{
		Use:   "proposal-submit PROJECT_ID",
		Short: "Submit an independent, Run-optional Evidence Proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := strings.TrimSpace(args[0])
			if strings.TrimSpace(summary) == "" {
				return fmt.Errorf("--summary is required")
			}
			if strings.TrimSpace(patchJSON) == "" {
				return fmt.Errorf("--patch-json is required")
			}
			var patch store.EvidenceGraphPatch
			if err := json.Unmarshal([]byte(patchJSON), &patch); err != nil {
				return fmt.Errorf("decode --patch-json: %w", err)
			}
			if strings.TrimSpace(targetMapID) != "" {
				patch.ChainID = strings.TrimSpace(targetMapID)
			}
			if strings.TrimSpace(routingReason) != "" {
				patch.RoutingReason = strings.TrimSpace(routingReason)
			}
			db := openDB()
			defer db.Close()
			proposal, err := db.CreateEvidenceProposal(cmd.Context(), &store.EvidenceProposal{
				ProjectID:          projectID,
				TargetChainID:      strings.TrimSpace(targetMapID),
				Actor:              strings.TrimSpace(actor),
				Summary:            strings.TrimSpace(summary),
				RoutingReason:      strings.TrimSpace(routingReason),
				ProjectLevelImpact: projectLevelImpact,
				SourceRunIDs:       sourceRunIDs,
				SourceSnapshotIDs:  sourceSnapshotIDs,
			}, &patch)
			if err != nil {
				return err
			}
			if asJSON {
				view, viewErr := buildEvidenceProposalCLIView(cmd.Context(), db, proposal)
				if viewErr != nil {
					return viewErr
				}
				return printJSON(view)
			}
			if proposal.Status == store.GraphProposalDraft {
				fmt.Printf("Saved unrouted Evidence Proposal %s as draft\n", proposal.ID)
			} else {
				fmt.Printf("Submitted Evidence Proposal %s to Map %s at revision %d\n", proposal.ID, proposal.TargetChainID, proposal.BaseGraphRevision)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&targetMapID, "target-map", "", "Explicit target Map id; omit only to save an unrouted draft")
	cmd.Flags().StringVar(&actor, "actor", "agent", "Proposal author identity")
	cmd.Flags().StringVar(&summary, "summary", "", "Short human-readable proposal summary")
	cmd.Flags().StringVar(&routingReason, "routing-reason", "", "Why this Topic Map owns the proposed change")
	cmd.Flags().StringVar(&patchJSON, "patch-json", "", "Additive patch JSON with nodes and edges")
	cmd.Flags().StringSliceVar(&sourceRunIDs, "source-run", nil, "Source Run id (repeatable; optional)")
	cmd.Flags().StringSliceVar(&sourceSnapshotIDs, "source-snapshot", nil, "Source Evidence Snapshot id (repeatable; optional)")
	cmd.Flags().BoolVar(&projectLevelImpact, "project-level-impact", false, "Declare that a direct Primary Map proposal changes the project-level decision")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposalListCmd() *cobra.Command {
	var status string
	var limit, offset int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "proposal-list PROJECT_ID",
		Short: "List independent Evidence Proposals for a Project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			proposals, err := db.ListEvidenceProposals(cmd.Context(), store.EvidenceProposalFilter{
				ProjectID: strings.TrimSpace(args[0]),
				Status:    strings.TrimSpace(status),
				Limit:     limit,
				Offset:    offset,
			})
			if err != nil {
				return err
			}
			if asJSON {
				views := make([]evidenceProposalCLIView, 0, len(proposals))
				for index := range proposals {
					view, viewErr := buildEvidenceProposalCLIView(cmd.Context(), db, &proposals[index])
					if viewErr != nil {
						return viewErr
					}
					views = append(views, *view)
				}
				return printJSON(views)
			}
			for _, proposal := range proposals {
				fmt.Printf("%s\t%s\t%s\t%s\n", proposal.ID, proposal.Status, proposal.TargetChainID, proposal.Summary)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Filter by draft, pending, accepted, rejected, expired, or conflicted")
	cmd.Flags().IntVar(&limit, "limit", 80, "Maximum proposals")
	cmd.Flags().IntVar(&offset, "offset", 0, "Number of proposals to skip")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposalGetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "proposal-get PROPOSAL_ID",
		Short: "Read one independent Evidence Proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			proposal, err := db.GetEvidenceProposal(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if proposal == nil {
				return fmt.Errorf("evidence proposal %q not found", args[0])
			}
			if asJSON {
				view, viewErr := buildEvidenceProposalCLIView(cmd.Context(), db, proposal)
				if viewErr != nil {
					return viewErr
				}
				return printJSON(view)
			}
			fmt.Printf("%s status=%s project=%s target=%s base=%d\n", proposal.ID, proposal.Status, proposal.ProjectID, proposal.TargetChainID, proposal.BaseGraphRevision)
			fmt.Println(proposal.Summary)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposalPlanCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "proposal-plan PROPOSAL_ID_OR_RUN_ID",
		Short: "Plan proposal acceptance without changing the graph",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			proposal, err := db.GetEvidenceProposal(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var plan *store.EvidenceGraphProposalPlan
			if proposal != nil {
				plan, err = db.PlanEvidenceProposal(cmd.Context(), args[0])
			} else {
				plan, err = db.PlanEvidenceGraphProposal(cmd.Context(), args[0])
			}
			if err != nil {
				return err
			}
			if plan == nil {
				return fmt.Errorf("evidence proposal or project run card for %q not found", args[0])
			}
			if asJSON {
				return printJSON(plan)
			}
			fmt.Printf("proposal %s status=%s eligible=%t base=%d current=%d\n", plan.ProposalHash, plan.Status, plan.Eligible, plan.BaseGraphRevision, plan.CurrentGraphRevision)
			for _, blocker := range plan.Blockers {
				fmt.Printf("- %s: %s\n", blocker.Code, blocker.Message)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposalReviewCmd() *cobra.Command {
	var action, reviewer string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "proposal-review PROPOSAL_ID_OR_RUN_ID",
		Short: "Accept, reject, or expire a pending graph proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(action) == "" {
				return fmt.Errorf("--action is required")
			}
			db := openDB()
			defer db.Close()
			proposal, err := db.GetEvidenceProposal(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if proposal != nil {
				reviewed, reviewErr := db.ReviewEvidenceProposal(cmd.Context(), args[0], action, reviewer)
				if reviewErr != nil {
					return reviewErr
				}
				if asJSON {
					view, viewErr := buildEvidenceProposalCLIView(cmd.Context(), db, reviewed)
					if viewErr != nil {
						return viewErr
					}
					return printJSON(view)
				}
				fmt.Printf("Evidence Proposal %s is %s\n", reviewed.ID, reviewed.Status)
				return nil
			}
			card, err := db.ReviewEvidenceGraphProposal(cmd.Context(), args[0], action, reviewer)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(card)
			}
			fmt.Printf("Graph proposal for %s is %s\n", card.RunID, card.GraphStatus)
			return nil
		},
	}
	cmd.Flags().StringVar(&action, "action", "", "Review action: accept, reject, or expire")
	cmd.Flags().StringVar(&reviewer, "reviewer", "user", "Reviewer identity recorded in the graph revision")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceProposalRerouteCmd() *cobra.Command {
	var targetMapID, routingReason string
	var projectLevelImpact, asJSON bool
	cmd := &cobra.Command{
		Use:   "proposal-reroute PROPOSAL_ID",
		Short: "Route a draft or pending Proposal to another explicit Map",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(targetMapID) == "" {
				return fmt.Errorf("--target-map is required")
			}
			db := openDB()
			defer db.Close()
			proposal, err := db.RerouteEvidenceProposal(
				cmd.Context(), args[0], targetMapID, routingReason, projectLevelImpact,
			)
			if err != nil {
				return err
			}
			if asJSON {
				view, viewErr := buildEvidenceProposalCLIView(cmd.Context(), db, proposal)
				if viewErr != nil {
					return viewErr
				}
				return printJSON(view)
			}
			fmt.Printf("Rerouted Proposal %s to Map %s at revision %d\n", proposal.ID, proposal.TargetChainID, proposal.BaseGraphRevision)
			return nil
		},
	}
	cmd.Flags().StringVar(&targetMapID, "target-map", "", "New explicit target Map id")
	cmd.Flags().StringVar(&routingReason, "routing-reason", "", "Why the new Topic Map owns the change")
	cmd.Flags().BoolVar(&projectLevelImpact, "project-level-impact", false, "Declare project-level impact when routing to Primary")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidenceMigrateOrphansCmd() *cobra.Command {
	var mappingJSON string
	var apply, asJSON bool
	cmd := &cobra.Command{
		Use:   "migrate-orphans",
		Short: "Report or migrate active Evidence Maps without Project ownership",
		Long: `Report every active Evidence Map whose project_id is empty.

No ownership is inferred from titles. --mapping-json explicitly maps Map ids to
canonical Project ids. During --apply, mapped Maps are bound and every unmapped
Map is preserved as archived legacy history; nodes, edges, revisions, and hashes
are never deleted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mappings := map[string]string{}
			if strings.TrimSpace(mappingJSON) != "" {
				if err := json.Unmarshal([]byte(mappingJSON), &mappings); err != nil {
					return fmt.Errorf("decode --mapping-json: %w", err)
				}
			}
			db := openDB()
			defer db.Close()
			var report *store.EvidenceMapOwnershipMigrationReport
			var err error
			if apply {
				report, err = db.ApplyEvidenceMapOwnershipMigration(cmd.Context(), mappings)
			} else {
				report, err = db.PlanEvidenceMapOwnershipMigration(cmd.Context(), mappings)
			}
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(report)
			}
			mode := "DRY RUN"
			if apply {
				mode = "APPLIED"
			}
			fmt.Printf("%s: %d orphan Maps; %d bind; %d archive legacy\n", mode, report.OrphanCount, report.BoundCount, report.ArchivedCount)
			for _, entry := range report.Entries {
				fmt.Printf("- %s\t%s\tproject=%s\tnodes=%d\tedges=%d\trevision=%d\n",
					entry.MapID, entry.Action, entry.ProjectID, entry.NodeCount, entry.EdgeCount, entry.Revision)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&mappingJSON, "mapping-json", "{}", `Explicit Map-to-Project JSON, e.g. {"chain_x":"project_a"}`)
	cmd.Flags().BoolVar(&apply, "apply", false, "Apply the displayed policy transactionally; default is report-only")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the migration report as JSON")
	return cmd
}

func evidencePromotionPlanCmd() *cobra.Command {
	var sourceNodeIDs []string
	var summary, nodeType, actor string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "promotion-plan TOPIC_MAP_ID",
		Short: "Plan a Topic-to-Primary summary and Map Reference without side effects",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			plan, err := db.PlanEvidencePromotion(cmd.Context(), store.EvidencePromotionRequest{
				SourceMapID: args[0], SourceNodeIDs: sourceNodeIDs, Summary: summary, NodeType: nodeType, Actor: actor,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(plan)
			}
			fmt.Printf("promotion plan %s eligible=%t source=%s@%d target=%s@%d\n",
				plan.PlanHash, plan.Eligible, plan.SourceMapID, plan.SourceRevision, plan.TargetPrimaryMapID, plan.TargetPrimaryRevision)
			for _, blocker := range plan.Blockers {
				fmt.Printf("- %s: %s\n", blocker.Code, blocker.Message)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&sourceNodeIDs, "source-node", nil, "Accepted Topic node id to summarize (repeatable)")
	cmd.Flags().StringVar(&summary, "summary", "", "Project-level summary")
	cmd.Flags().StringVar(&nodeType, "node-type", "claim", "Primary summary type: claim, issue, or plan")
	cmd.Flags().StringVar(&actor, "actor", "agent", "Proposal author identity")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func evidencePromotionCreateCmd() *cobra.Command {
	var sourceNodeIDs []string
	var summary, nodeType, actor, expectedPlanHash string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "promotion-create TOPIC_MAP_ID",
		Short: "Create a reviewable Primary Proposal from an accepted Topic revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(expectedPlanHash) == "" {
				return fmt.Errorf("--expected-plan-hash is required")
			}
			db := openDB()
			defer db.Close()
			proposal, err := db.CreateEvidencePromotion(cmd.Context(), store.EvidencePromotionRequest{
				SourceMapID: args[0], SourceNodeIDs: sourceNodeIDs, Summary: summary, NodeType: nodeType, Actor: actor,
			}, expectedPlanHash)
			if err != nil {
				return err
			}
			if asJSON {
				view, viewErr := buildEvidenceProposalCLIView(cmd.Context(), db, proposal)
				if viewErr != nil {
					return viewErr
				}
				return printJSON(view)
			}
			fmt.Printf("Created promotion Proposal %s for Primary Map %s\n", proposal.ID, proposal.TargetChainID)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&sourceNodeIDs, "source-node", nil, "Accepted Topic node id to summarize (repeatable)")
	cmd.Flags().StringVar(&summary, "summary", "", "Project-level summary")
	cmd.Flags().StringVar(&nodeType, "node-type", "claim", "Primary summary type: claim, issue, or plan")
	cmd.Flags().StringVar(&actor, "actor", "agent", "Proposal author identity")
	cmd.Flags().StringVar(&expectedPlanHash, "expected-plan-hash", "", "Exact hash returned by promotion-plan")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func matrixCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "matrix",
		Short: "Read and edit Experiment Matrices",
		Long: `Read and edit Experiment Matrices.

An Experiment Matrix is a plain table: each row is an experiment/run, columns
are aligned fields such as run_id, val_loss, test_mse, conclusion, and notes.
Agents should write cells through this command instead of editing SQLite
directly.`,
	}
	cmd.AddCommand(matrixListCmd())
	cmd.AddCommand(matrixCreateCmd())
	cmd.AddCommand(matrixShowCmd())
	cmd.AddCommand(matrixSetCmd())
	return cmd
}

func matrixListCmd() *cobra.Command {
	var query string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Experiment Matrices",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			matrices, err := db.ListExperimentMatrices(cmd.Context(), store.ExperimentMatrixFilter{Query: query, Limit: limit})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(matrices)
			}
			if len(matrices) == 0 {
				fmt.Println("No Experiment Matrices yet.")
				return nil
			}
			for _, matrix := range matrices {
				fmt.Printf("%s\t%s", matrix.ID, matrix.Title)
				if matrix.Description != "" {
					fmt.Printf("\t%s", evidencePreview(matrix.Description, 90))
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "Search matrix id, title, or description")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum matrices to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func matrixCreateCmd() *cobra.Command {
	var description string
	var columns []string
	var noDefaults bool
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Create an Experiment Matrix",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.TrimSpace(args[0])
			if title == "" {
				return fmt.Errorf("title is required")
			}
			db := openDB()
			defer db.Close()
			matrix := &store.ExperimentMatrix{
				ID:          genID("matrix_"),
				Title:       title,
				Description: strings.TrimSpace(description),
				DataJSON:    "{}",
			}
			if err := db.CreateExperimentMatrix(cmd.Context(), matrix); err != nil {
				return err
			}
			if !noDefaults {
				if len(columns) == 0 {
					columns = []string{"run_id", "metric_1", "metric_2", "实验时间"}
				}
				grid := store.ExperimentMatrixGrid{
					Rows: []store.ExperimentMatrixRow{{
						ID:       genID("mrow_"),
						Label:    "实验 1",
						Position: 0,
						DataJSON: "{}",
					}},
				}
				for i, label := range columns {
					label = strings.TrimSpace(label)
					if label == "" {
						continue
					}
					grid.Columns = append(grid.Columns, store.ExperimentMatrixColumn{
						ID:       genID("mcol_"),
						Label:    label,
						Position: i,
						DataJSON: "{}",
					})
				}
				if err := db.SaveExperimentMatrixGrid(cmd.Context(), matrix.ID, grid); err != nil {
					return err
				}
			}
			if asJSON {
				detail, err := loadExperimentMatrixDetail(cmd.Context(), db, matrix.ID)
				if err != nil {
					return err
				}
				return printJSON(detail)
			}
			fmt.Printf("Created Experiment Matrix %s: %s\n", matrix.ID, matrix.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "Short description")
	cmd.Flags().StringArrayVar(&columns, "column", nil, "Initial column label (repeatable). Defaults to run_id, metric_1, metric_2, 实验时间")
	cmd.Flags().BoolVar(&noDefaults, "no-defaults", false, "Create an empty matrix with no rows or columns")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func matrixShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [matrix_id]",
		Short: "Show an Experiment Matrix",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			detail, err := loadExperimentMatrixDetail(cmd.Context(), db, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(detail)
			}
			printExperimentMatrixTable(detail)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func matrixSetCmd() *cobra.Command {
	var rowLabel, columnLabel, value, runID, projectCardID string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "set [matrix_id]",
		Short: "Set one Experiment Matrix cell by row and column label",
		Long: `Set one Experiment Matrix cell by row and column label.

Missing rows and columns are created automatically. Use --run-id for an
experiment reference. For a run_id column, --value may also be the run id.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rowLabel = strings.TrimSpace(rowLabel)
			columnLabel = strings.TrimSpace(columnLabel)
			value = strings.TrimSpace(value)
			runID = strings.TrimSpace(runID)
			projectCardID = strings.TrimSpace(projectCardID)
			if rowLabel == "" || columnLabel == "" {
				return fmt.Errorf("--row and --column are required")
			}
			if value == "" && runID == "" {
				return fmt.Errorf("--value or --run-id is required")
			}
			db := openDB()
			defer db.Close()
			matrix, grid, err := loadExperimentMatrixGridForEdit(cmd.Context(), db, args[0])
			if err != nil {
				return err
			}
			if runID == "" && isRunIDMatrixColumn(columnLabel) {
				runID = value
			}
			if runID != "" {
				run, err := db.GetRun(cmd.Context(), runID)
				if err != nil {
					return err
				}
				if run == nil {
					return fmt.Errorf("run %q does not exist", runID)
				}
				if value == "" || isRunIDMatrixColumn(columnLabel) {
					value = runID
				}
				if projectCardID == "" {
					if card, _ := db.GetProjectRunCard(cmd.Context(), runID); card != nil {
						projectCardID = card.ID
					}
				}
			}
			row := ensureExperimentMatrixRow(grid, rowLabel)
			column := ensureExperimentMatrixColumn(grid, columnLabel)
			cell := upsertExperimentMatrixCell(grid, matrix.ID, row.ID, column.ID, column.Label, value, runID, projectCardID)
			if err := db.SaveExperimentMatrixGrid(cmd.Context(), matrix.ID, *grid); err != nil {
				return err
			}
			if asJSON {
				if detail, err := loadExperimentMatrixDetail(cmd.Context(), db, matrix.ID); err == nil {
					if saved, ok := experimentMatrixCellByID(detail.Cells, cell.ID); ok {
						cell = saved
					}
				}
				return printJSON(cell)
			}
			fmt.Printf("Set %s [%s, %s] = %s\n", matrix.ID, row.Label, column.Label, value)
			if runID != "" {
				fmt.Printf("run_id: %s\n", runID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rowLabel, "row", "", "Experiment row label, e.g. trial022 seed2021")
	cmd.Flags().StringVar(&columnLabel, "column", "", "Column label, e.g. run_id, val_loss, conclusion")
	cmd.Flags().StringVar(&value, "value", "", "Cell value")
	cmd.Flags().StringVar(&runID, "run-id", "", "Run id to attach to this cell")
	cmd.Flags().StringVar(&projectCardID, "project-card-id", "", "Optional project card id associated with the run")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func loadExperimentMatrixDetail(ctx context.Context, db *store.SQLite, matrixID string) (*store.ExperimentMatrixDetail, error) {
	matrix, grid, err := loadExperimentMatrixGridForEdit(ctx, db, matrixID)
	if err != nil {
		return nil, err
	}
	return &store.ExperimentMatrixDetail{
		ExperimentMatrix: *matrix,
		Rows:             grid.Rows,
		Columns:          grid.Columns,
		Cells:            grid.Cells,
	}, nil
}

func loadExperimentMatrixGridForEdit(ctx context.Context, db *store.SQLite, matrixID string) (*store.ExperimentMatrix, *store.ExperimentMatrixGrid, error) {
	matrixID = strings.TrimSpace(matrixID)
	if matrixID == "" {
		return nil, nil, fmt.Errorf("matrix_id is required")
	}
	matrix, err := db.GetExperimentMatrix(ctx, matrixID)
	if err != nil {
		return nil, nil, err
	}
	if matrix == nil {
		return nil, nil, fmt.Errorf("experiment matrix %q not found", matrixID)
	}
	grid, err := db.GetExperimentMatrixGrid(ctx, matrixID)
	if err != nil {
		return nil, nil, err
	}
	if grid == nil {
		grid = &store.ExperimentMatrixGrid{}
	}
	return matrix, grid, nil
}

func ensureExperimentMatrixRow(grid *store.ExperimentMatrixGrid, label string) store.ExperimentMatrixRow {
	for _, row := range grid.Rows {
		if strings.EqualFold(strings.TrimSpace(row.Label), label) {
			return row
		}
	}
	row := store.ExperimentMatrixRow{
		ID:       genID("mrow_"),
		Label:    label,
		Position: len(grid.Rows),
		DataJSON: "{}",
	}
	grid.Rows = append(grid.Rows, row)
	return row
}

func ensureExperimentMatrixColumn(grid *store.ExperimentMatrixGrid, label string) store.ExperimentMatrixColumn {
	for _, column := range grid.Columns {
		if strings.EqualFold(strings.TrimSpace(column.Label), label) {
			return column
		}
	}
	column := store.ExperimentMatrixColumn{
		ID:       genID("mcol_"),
		Label:    label,
		Position: len(grid.Columns),
		DataJSON: "{}",
	}
	grid.Columns = append(grid.Columns, column)
	return column
}

func upsertExperimentMatrixCell(grid *store.ExperimentMatrixGrid, matrixID, rowID, columnID, metricKey, value, runID, projectCardID string) store.ExperimentMatrixCell {
	cell := store.ExperimentMatrixCell{
		ID:            genID("mcell_"),
		MatrixID:      matrixID,
		RowID:         rowID,
		ColumnID:      columnID,
		RunID:         runID,
		ProjectCardID: projectCardID,
		MetricKey:     metricKey,
		MetricValue:   value,
		DataJSON:      "{}",
	}
	next := grid.Cells[:0]
	for _, existing := range grid.Cells {
		if existing.RowID == rowID && existing.ColumnID == columnID {
			cell.ID = existing.ID
			cell.Title = existing.Title
			cell.Statement = existing.Statement
			cell.Note = existing.Note
			if cell.RunID == "" {
				cell.RunID = existing.RunID
			}
			if cell.ProjectCardID == "" {
				cell.ProjectCardID = existing.ProjectCardID
			}
			if existing.DataJSON != "" && existing.DataJSON != "{}" {
				cell.DataJSON = existing.DataJSON
			}
			continue
		}
		next = append(next, existing)
	}
	next = append(next, cell)
	grid.Cells = next
	return cell
}

func printExperimentMatrixTable(detail *store.ExperimentMatrixDetail) {
	fmt.Printf("# %s (%s)\n", detail.Title, detail.ID)
	if detail.Description != "" {
		fmt.Println(detail.Description)
	}
	fmt.Printf("\nRows: %d  Columns: %d  Cells: %d\n\n", len(detail.Rows), len(detail.Columns), len(detail.Cells))
	fmt.Print("实验名称")
	for _, column := range detail.Columns {
		fmt.Printf("\t%s", column.Label)
	}
	fmt.Println()
	for _, row := range detail.Rows {
		fmt.Print(row.Label)
		for _, column := range detail.Columns {
			fmt.Printf("\t%s", experimentMatrixCellDisplay(detail.Cells, row.ID, column.ID, column.Label))
		}
		fmt.Println()
	}
}

func experimentMatrixCellDisplay(cells []store.ExperimentMatrixCell, rowID, columnID, columnLabel string) string {
	for _, cell := range cells {
		if cell.RowID == rowID && cell.ColumnID == columnID {
			if isRunIDMatrixColumn(columnLabel) && cell.RunID != "" {
				return cell.RunID
			}
			return firstNonEmpty(cell.MetricValue, cell.Statement, cell.Title, cell.Note, cell.RunID)
		}
	}
	return ""
}

func experimentMatrixCellByID(cells []store.ExperimentMatrixCell, cellID string) (store.ExperimentMatrixCell, bool) {
	for _, cell := range cells {
		if cell.ID == cellID {
			return cell, true
		}
	}
	return store.ExperimentMatrixCell{}, false
}

func isRunIDMatrixColumn(label string) bool {
	return strings.EqualFold(strings.ReplaceAll(strings.TrimSpace(label), " ", "_"), "run_id")
}

func buildEvidenceChainAgentSnapshot(ctx context.Context, db *store.SQLite, chainID string) (*evidenceChainAgentSnapshot, error) {
	chain, graph, err := loadEvidenceChainGraphForEdit(ctx, db, chainID)
	if err != nil {
		return nil, err
	}
	nodes := append([]store.EvidenceChainNode(nil), graph.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Y == nodes[j].Y {
			if nodes[i].X == nodes[j].X {
				return nodes[i].ID < nodes[j].ID
			}
			return nodes[i].X < nodes[j].X
		}
		return nodes[i].Y < nodes[j].Y
	})
	edges := append([]store.EvidenceChainEdge(nil), graph.Edges...)
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	snapshot := &evidenceChainAgentSnapshot{
		ChainID:      chain.ID,
		Title:        chain.Title,
		Description:  chain.Description,
		RoutingHints: chain.RoutingHints,
		ProjectID:    chain.ProjectID,
		Role:         chain.Role,
		Status:       chain.Status,
		Revision:     chain.Revision,
		GraphHash:    chain.GraphHash,
		Intro:        fmt.Sprintf("%s: %d nodes, %d typed edges", chain.Title, len(nodes), len(edges)),
		UpdatedAt:    chain.UpdatedAt,
		Nodes:        make([]evidenceChainAgentNode, 0, len(nodes)),
		Edges:        make([]evidenceChainAgentEdge, 0, len(edges)),
	}
	for _, node := range nodes {
		view := evidenceChainAgentNode{
			NodeID:        node.ID,
			Type:          node.Type,
			Title:         node.Title,
			Body:          node.Body,
			RunID:         node.RunID,
			ProjectCardID: node.ProjectCardID,
		}
		var run *store.Run
		var card *store.ProjectRunCard
		if node.RunID != "" {
			run, _ = db.GetRun(ctx, node.RunID)
			if run != nil {
				view.Run = &evidenceChainRunSummary{
					ID:         run.ID,
					Name:       run.Name,
					Status:     run.Status,
					Kind:       run.Kind,
					ResourceID: run.ResourceID,
					Command:    evidencePreview(run.Command, 220),
					Cwd:        firstNonEmpty(run.ResolvedCwd, run.Cwd),
					GitBranch:  run.GitBranch,
					GitCommit:  shortGitCommit(run.GitCommit),
					GitDirty:   run.GitDirty,
				}
			}
			card, _ = db.GetProjectRunCard(ctx, node.RunID)
			if card != nil {
				view.ProjectCard = &evidenceChainProjectSummary{
					ID:            card.ID,
					ProjectID:     card.ProjectID,
					ProjectName:   card.ProjectName,
					Question:      card.Question,
					Verdict:       card.Verdict,
					EvidenceLevel: card.EvidenceLevel,
					KeyMetrics:    card.KeyMetrics,
					NextAction:    card.NextAction,
					SupportsClaim: card.SupportsClaim,
					WeakensClaim:  card.WeakensClaim,
				}
			}
			marks, _ := db.ListRunMarks(ctx, store.RunMarkFilter{RunID: node.RunID, Limit: 8})
			for _, mark := range marks {
				view.Marks = append(view.Marks, evidenceChainMarkSummary{
					ID:        mark.ID,
					Actor:     mark.Actor,
					Kind:      mark.Kind,
					Title:     mark.Title,
					Statement: mark.Statement,
				})
			}
		}
		view.ShortIntro = evidenceNodeShortIntro(node, run, card)
		snapshot.Nodes = append(snapshot.Nodes, view)
	}
	for _, edge := range edges {
		snapshot.Edges = append(snapshot.Edges, evidenceChainAgentEdge{
			EdgeID:     edge.ID,
			FromNodeID: edge.SourceNodeID,
			ToNodeID:   edge.TargetNodeID,
			Type:       edge.Type,
			Label:      edge.Label,
			Rationale:  edge.Rationale,
		})
	}
	return snapshot, nil
}

func printEvidenceChainSnapshot(snapshot *evidenceChainAgentSnapshot) {
	fmt.Printf("# %s (%s)\n", snapshot.Title, snapshot.ChainID)
	if snapshot.Description != "" {
		fmt.Println(snapshot.Description)
	}
	if len(snapshot.RoutingHints.Recipes) > 0 {
		fmt.Printf("Recipes: %s\n", strings.Join(snapshot.RoutingHints.Recipes, ", "))
	}
	if len(snapshot.RoutingHints.Keywords) > 0 {
		fmt.Printf("Keywords: %s\n", strings.Join(snapshot.RoutingHints.Keywords, ", "))
	}
	fmt.Printf("\nNodes: %d  Edges: %d\n\n", len(snapshot.Nodes), len(snapshot.Edges))
	for _, node := range snapshot.Nodes {
		fmt.Printf("- %s [%s] %s\n", node.NodeID, node.Type, firstNonEmpty(node.Title, node.ShortIntro))
		if node.RunID != "" {
			fmt.Printf("  run_id: %s\n", node.RunID)
		}
		if node.ShortIntro != "" {
			fmt.Printf("  intro: %s\n", node.ShortIntro)
		}
	}
	if len(snapshot.Edges) > 0 {
		fmt.Println("\nEdges:")
		for _, edge := range snapshot.Edges {
			label := firstNonEmpty(edge.Label, defaultEvidenceEdgeLabel(edge.Type))
			fmt.Printf("- %s: %s --%s/%s--> %s\n", edge.EdgeID, edge.FromNodeID, edge.Type, label, edge.ToNodeID)
			if edge.Rationale != "" {
				fmt.Printf("  rationale: %s\n", edge.Rationale)
			}
		}
	}
}

func loadEvidenceChainGraphForEdit(ctx context.Context, db *store.SQLite, chainID string) (*store.EvidenceChain, *store.EvidenceChainGraph, error) {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return nil, nil, fmt.Errorf("chain_id is required")
	}
	chain, err := db.GetEvidenceChain(ctx, chainID)
	if err != nil {
		return nil, nil, err
	}
	if chain == nil {
		return nil, nil, fmt.Errorf("evidence chain %q not found", chainID)
	}
	graph, err := db.GetEvidenceChainGraph(ctx, chainID)
	if err != nil {
		return nil, nil, err
	}
	if graph == nil {
		graph = &store.EvidenceChainGraph{}
	}
	return chain, graph, nil
}

func ctxGraph(ctx context.Context, db *store.SQLite, chainID string) *store.EvidenceChainGraph {
	graph, err := db.GetEvidenceChainGraph(ctx, chainID)
	if err != nil {
		return nil
	}
	return graph
}

func evidenceNodeByID(graph *store.EvidenceChainGraph, id string) (store.EvidenceChainNode, bool) {
	if graph == nil {
		return store.EvidenceChainNode{}, false
	}
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return store.EvidenceChainNode{}, false
}

func evidenceEdgeByID(graph *store.EvidenceChainGraph, id string) (store.EvidenceChainEdge, bool) {
	if graph == nil {
		return store.EvidenceChainEdge{}, false
	}
	for _, edge := range graph.Edges {
		if edge.ID == id {
			return edge, true
		}
	}
	return store.EvidenceChainEdge{}, false
}

func validEvidenceNodeTypeForCLI(t string) bool {
	return store.ValidEvidenceNodeType(t)
}

func validEvidenceEdgeTypeForCLI(t string) bool {
	return store.ValidEvidenceEdgeType(t)
}

func evidenceNodeExists(nodes []store.EvidenceChainNode, id string) bool {
	for _, node := range nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func nextEvidenceNodePosition(nodes []store.EvidenceChainNode) (float64, float64) {
	const (
		startX = 80.0
		startY = 80.0
		stepX  = 320.0
		stepY  = 220.0
		cols   = 4
	)
	occupied := map[string]bool{}
	for _, node := range nodes {
		col := int((node.X - startX + stepX/2) / stepX)
		row := int((node.Y - startY + stepY/2) / stepY)
		if col >= 0 && row >= 0 {
			occupied[fmt.Sprintf("%d:%d", col, row)] = true
		}
	}
	for idx := 0; ; idx++ {
		col := idx % cols
		row := idx / cols
		key := fmt.Sprintf("%d:%d", col, row)
		if !occupied[key] {
			return startX + float64(col)*stepX, startY + float64(row)*stepY
		}
	}
}

func evidenceDefaultNodeTitle(nodeType string, run *store.Run, card *store.ProjectRunCard) string {
	if nodeType == store.EvidenceNodeRun {
		if card != nil {
			return firstNonEmpty(card.Verdict, card.Question, card.RunID)
		}
		if run != nil {
			return firstNonEmpty(run.Name, run.ID)
		}
	}
	switch nodeType {
	case store.EvidenceNodeHypothesis:
		return "Hypothesis"
	case store.EvidenceNodeExperiment:
		return "Experiment"
	case store.EvidenceNodePlan:
		return "Plan"
	case store.EvidenceNodeConclusion:
		return "Conclusion"
	default:
		return "Note"
	}
}

func evidenceNodeShortIntro(node store.EvidenceChainNode, run *store.Run, card *store.ProjectRunCard) string {
	if node.Type != store.EvidenceNodeRun {
		return evidencePreview(firstNonEmpty(node.Body, node.Title), 220)
	}
	parts := []string{node.RunID}
	if run != nil {
		parts = append(parts, run.Name, run.Kind, run.Status, runGitLabel(run))
	}
	if card != nil {
		parts = append(parts, card.ProjectName, card.EvidenceLevel, card.Verdict, card.Question, firstLine(card.KeyMetrics), card.NextAction)
	}
	return evidencePreview(joinNonEmpty(" · ", parts...), 260)
}

func runGitLabel(run *store.Run) string {
	if run == nil || run.GitCommit == "" {
		return ""
	}
	label := shortGitCommit(run.GitCommit)
	if run.GitBranch != "" {
		label = run.GitBranch + "@" + label
	}
	if run.GitDirty {
		label += " dirty"
	}
	return "git " + label
}

func shortGitCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func defaultEvidenceEdgeLabel(edgeType string) string {
	switch edgeType {
	case store.EvidenceEdgeSupports:
		return "supports"
	case store.EvidenceEdgeDoesNotProve:
		return "does not prove"
	case store.EvidenceEdgeNextStep:
		return "next step"
	default:
		return "custom"
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func evidencePreview(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func loadProjectFileConfig(path string) (*projectFileConfig, error) {
	resolved, err := resolveProjectConfigPath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read project config: %w", err)
	}
	cfg := &projectFileConfig{
		Path:             resolved,
		Commands:         map[string]projectFileCommand{},
		ResourceProfiles: map[string]projectResourceProfile{},
		FreezeProfiles:   map[string]freezer.Profile{},
	}
	section := ""
	listKey := ""
	resourceProfileName := ""
	resourceProfileSubsection := ""
	originalLines := strings.Split(string(data), "\n")
	lines := normalizeProjectConfigLines(originalLines)
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		raw := lines[i]
		line := stripProjectConfigComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			value := cleanProjectConfigValue(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			addProjectConfigListValue(cfg, section, listKey, value)
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected key: value", resolved, lineNo)
		}
		key = strings.TrimSpace(key)
		value = cleanProjectConfigValue(value)
		if isProjectBlockScalar(value) {
			value, i = collectProjectBlockScalar(lines, i+1, indent)
		}
		if indent == 0 {
			section = ""
			listKey = ""
			resourceProfileName = ""
			resourceProfileSubsection = ""
			if value == "" {
				switch normalizeProjectKey(key) {
				case "logs", "logpaths", "metrics", "metricpaths", "artifacts", "artifactpaths":
					listKey = key
				case "sync", "project", "resourceprofiles":
					section = key
				default:
					section = key
					ensureProjectCommand(cfg, section)
				}
				continue
			}
			if err := setProjectConfigScalar(cfg, "", key, value); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", resolved, lineNo, err)
			}
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("%s:%d: nested field without a section", resolved, lineNo)
		}
		listKey = ""
		if normalizeProjectKey(section) == "resourceprofiles" {
			if indent <= 2 {
				resourceProfileName = key
				resourceProfileSubsection = ""
				ensureProjectResourceProfile(cfg, resourceProfileName)
				if value != "" {
					cfg.Warnings = append(cfg.Warnings, "resource profile shorthand ignored: "+key)
				}
				continue
			}
			if resourceProfileName == "" {
				return nil, fmt.Errorf("%s:%d: resource profile field without a profile name", resolved, lineNo)
			}
			if resourceProfileSubsection == "env" && indent >= 6 && value != "" {
				setProjectResourceProfileEnv(cfg, resourceProfileName, key, value)
				continue
			}
			if value == "" {
				switch normalizeProjectKey(key) {
				case "env", "envvars", "environment":
					resourceProfileSubsection = "env"
				default:
					cfg.Warnings = append(cfg.Warnings, "unknown resource profile block ignored: "+resourceProfileName+"."+key)
					resourceProfileSubsection = ""
				}
				continue
			}
			resourceProfileSubsection = ""
			if err := setProjectResourceProfileScalar(cfg, resourceProfileName, key, value); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", resolved, lineNo, err)
			}
			continue
		}
		if value == "" {
			listKey = key
			continue
		}
		if err := setProjectConfigScalar(cfg, section, key, value); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", resolved, lineNo, err)
		}
	}
	if cfg.Sync.Profile == "" {
		cfg.Sync.Profile = "code"
	}
	if err := parseProjectFreezeProfiles(originalLines, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", resolved, err)
	}
	parseProjectCommandInputs(originalLines, cfg)
	return cfg, nil
}

func normalizeProjectConfigLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	skipFreeze, inCommands := false, false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(stripProjectConfigComment(raw))
		indent := leadingSpaces(raw)
		if indent == 0 {
			skipFreeze = normalizeProjectKey(strings.TrimSuffix(trimmed, ":")) == "freezeprofiles"
			inCommands = normalizeProjectKey(strings.TrimSuffix(trimmed, ":")) == "commands"
			if skipFreeze || inCommands {
				continue
			}
		}
		if skipFreeze {
			continue
		}
		if inCommands && len(raw) >= 2 {
			out = append(out, raw[2:])
			continue
		}
		out = append(out, raw)
	}
	return out
}

func parseProjectFreezeProfiles(lines []string, cfg *projectFileConfig) error {
	inFreeze := false
	profileName, subsection, role := "", "", ""
	for i, raw := range lines {
		line := stripProjectConfigComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if indent == 0 {
			inFreeze = normalizeProjectKey(strings.TrimSuffix(trimmed, ":")) == "freezeprofiles"
			profileName, subsection, role = "", "", ""
			continue
		}
		if !inFreeze {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			value := cleanProjectConfigValue(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			p := cfg.FreezeProfiles[profileName]
			switch subsection {
			case "workspaceroles":
				p.WorkspaceRoles = append(p.WorkspaceRoles, value)
			case "required", "optional":
				for idx := range p.Rules {
					if p.Rules[idx].Role == role && p.Rules[idx].Required == (subsection == "required") {
						p.Rules[idx].Patterns = append(p.Rules[idx].Patterns, value)
					}
				}
			case "aggregateoutputs":
				p.AggregateOutputs = append(p.AggregateOutputs, value)
			}
			cfg.FreezeProfiles[profileName] = p
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return fmt.Errorf("line %d: expected freeze key: value", i+1)
		}
		key = strings.TrimSpace(key)
		value = cleanProjectConfigValue(value)
		if isProjectBlockScalar(value) {
			value, i = collectProjectBlockScalar(lines, i+1, indent)
		}
		if indent == 2 {
			profileName = key
			cfg.FreezeProfiles[profileName] = freezer.Profile{Name: profileName}
			subsection, role = "", ""
			continue
		}
		if profileName == "" {
			return fmt.Errorf("line %d: freeze field without profile", i+1)
		}
		p := cfg.FreezeProfiles[profileName]
		if indent == 4 {
			switch normalizeProjectKey(key) {
			case "storage":
				p.Storage = value
			case "storageprefix":
				p.StoragePrefix = value
			case "workspaceroles":
				subsection = "workspaceroles"
			case "required", "optional":
				subsection = normalizeProjectKey(key)
			case "aggregate":
				subsection = "aggregate"
			case "releasegate":
				subsection = "releasegate"
			default:
				cfg.Warnings = append(cfg.Warnings, "unknown freeze profile field ignored: "+profileName+"."+key)
			}
			cfg.FreezeProfiles[profileName] = p
			continue
		}
		if indent == 6 {
			switch subsection {
			case "required", "optional":
				role = key
				p.Rules = append(p.Rules, freezer.RoleRule{Role: key, Required: subsection == "required"})
			case "aggregate":
				if normalizeProjectKey(key) == "command" {
					p.AggregateCommand = value
				} else if normalizeProjectKey(key) == "outputs" {
					subsection = "aggregateoutputs"
				}
			case "releasegate":
				if normalizeProjectKey(key) == "command" {
					p.GateCommand = value
				} else if normalizeProjectKey(key) == "report" {
					p.GateReport = value
				}
			}
			cfg.FreezeProfiles[profileName] = p
		}
	}
	return nil
}

func parseProjectCommandInputs(lines []string, cfg *projectFileConfig) {
	inCommands := false
	command, subsection, listKey := "", "", ""
	consumedMetadataWarnings := map[string]bool{}
	var input *store.RunInputBinding
	var output *store.RunOutputBinding
	flush := func() {
		if command == "" {
			input, output = nil, nil
			return
		}
		entry := ensureProjectCommand(cfg, command)
		if input != nil && (input.LogicalURI != "" || input.TargetPath != "" || input.Revision != "") {
			if input.Mode == "" {
				input.Mode = "copy"
			}
			entry.InputBindings = append(entry.InputBindings, *input)
		}
		if output != nil && (output.SourcePattern != "" || output.LogicalURI != "") {
			entry.OutputBindings = append(entry.OutputBindings, *output)
		}
		cfg.Commands[command] = entry
		input, output = nil, nil
	}
	for _, raw := range lines {
		line := stripProjectConfigComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if indent == 0 {
			flush()
			key := strings.TrimSuffix(trimmed, ":")
			inCommands = normalizeProjectKey(key) == "commands"
			if !inCommands {
				command = key
			}
			subsection, listKey = "", ""
			continue
		}
		base := 0
		if inCommands {
			base = 2
			if indent == 2 {
				flush()
				command = strings.TrimSuffix(trimmed, ":")
				subsection, listKey = "", ""
				continue
			}
		}
		if command == "" {
			continue
		}
		rel := indent - base
		if strings.HasPrefix(trimmed, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if subsection == "inputs" && (listKey == "datasets" || listKey == "seeds") {
				value := cleanProjectConfigValue(item)
				entry := ensureProjectCommand(cfg, command)
				if listKey == "datasets" {
					entry.Datasets = append(entry.Datasets, value)
				} else if n, err := strconv.ParseInt(value, 10, 64); err == nil {
					entry.Seeds = append(entry.Seeds, n)
				}
				cfg.Commands[command] = entry
				continue
			}
			if subsection == "inputs" && (listKey == "files" || listKey == "managedinputs" || listKey == "runinputs" || listKey == "") {
				flush()
				input = &store.RunInputBinding{}
				setStructuredInputField(input, item)
				continue
			}
			if subsection == "outputs" {
				flush()
				output = &store.RunOutputBinding{}
				setStructuredOutputField(output, item)
				continue
			}
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		normalizedKey := normalizeProjectKey(strings.TrimSpace(key))
		value = cleanProjectConfigValue(value)
		if rel == 2 {
			if normalizedKey == "inputs" || normalizedKey == "managedinputs" || normalizedKey == "runinputs" {
				flush()
				subsection, listKey = "inputs", normalizedKey
				continue
			}
			if normalizedKey == "outputs" || normalizedKey == "runoutputs" {
				flush()
				subsection, listKey = "outputs", normalizedKey
				continue
			}
			flush()
			subsection, listKey = "", ""
			continue
		}
		if rel == 4 && subsection == "inputs" && input == nil {
			listKey = normalizedKey
			if normalizedKey == "seeds" || normalizedKey == "datasets" {
				consumedMetadataWarnings[command+"."+strings.TrimSpace(key)] = true
			}
			if value != "" && (normalizedKey == "seeds" || normalizedKey == "datasets") {
				for _, part := range strings.Split(strings.Trim(value, "[]"), ",") {
					entry := ensureProjectCommand(cfg, command)
					part = cleanProjectConfigValue(strings.TrimSpace(part))
					if normalizedKey == "datasets" && part != "" {
						entry.Datasets = append(entry.Datasets, part)
						cfg.Commands[command] = entry
					} else if n, err := strconv.ParseInt(part, 10, 64); err == nil {
						entry.Seeds = append(entry.Seeds, n)
						cfg.Commands[command] = entry
					}
				}
			}
			continue
		}
		if input != nil && subsection == "inputs" {
			setStructuredInputField(input, trimmed)
		} else if output != nil && subsection == "outputs" {
			setStructuredOutputField(output, trimmed)
		}
	}
	flush()
	for name, entry := range cfg.Commands {
		if len(entry.OutputBindings) > 0 {
			legacy := entry.Outputs[:0]
			for _, spec := range entry.Outputs {
				if strings.Count(spec, "|") >= 3 {
					legacy = append(legacy, spec)
				}
			}
			entry.Outputs = legacy
			cfg.Commands[name] = entry
		}
	}
	if hasStructuredBindings(cfg) {
		cfg.Warnings = filterStructuredBindingWarnings(cfg.Warnings)
	}
	cfg.Warnings = filterConsumedProjectMetadataWarnings(cfg.Warnings, consumedMetadataWarnings)
}

func filterConsumedProjectMetadataWarnings(warnings []string, consumed map[string]bool) []string {
	filtered := warnings[:0]
	for _, warning := range warnings {
		ignored := false
		for field := range consumed {
			if warning == "unknown recipe field ignored: "+field || warning == "unknown recipe list ignored: "+field {
				ignored = true
				break
			}
		}
		if !ignored {
			filtered = append(filtered, warning)
		}
	}
	return filtered
}

func setStructuredInputField(binding *store.RunInputBinding, field string) {
	key, value, ok := strings.Cut(field, ":")
	if !ok || binding == nil {
		return
	}
	value = cleanProjectConfigValue(value)
	switch normalizeProjectKey(key) {
	case "from", "uri", "logicaluri":
		binding.LogicalURI = value
	case "to", "target", "targetpath":
		binding.TargetPath = value
	case "revision", "sha256":
		binding.Revision = value
	case "mode":
		binding.Mode = value
	}
}

func setStructuredOutputField(binding *store.RunOutputBinding, field string) {
	key, value, ok := strings.Cut(field, ":")
	if !ok || binding == nil {
		return
	}
	value = cleanProjectConfigValue(value)
	switch normalizeProjectKey(key) {
	case "from", "pattern", "source", "sourcepattern":
		binding.SourcePattern = value
	case "to", "uri", "logicaluri":
		binding.LogicalURI = value
	case "role":
		binding.Role = value
	case "required":
		binding.Required = parseProjectBool(value)
	}
}

func hasStructuredBindings(cfg *projectFileConfig) bool {
	for _, command := range cfg.Commands {
		if len(command.InputBindings) > 0 || len(command.OutputBindings) > 0 {
			return true
		}
	}
	return false
}

func filterStructuredBindingWarnings(warnings []string) []string {
	filtered := warnings[:0]
	for _, warning := range warnings {
		if strings.Contains(warning, ".files") || strings.HasSuffix(warning, ".from") || strings.HasSuffix(warning, ".to") || strings.HasSuffix(warning, ".revision") || strings.HasSuffix(warning, ".mode") || strings.HasSuffix(warning, ".role") || strings.HasSuffix(warning, ".required") {
			continue
		}
		filtered = append(filtered, warning)
	}
	return filtered
}

func resolveProjectConfigPath(path string) (string, error) {
	if path != "" {
		path = expandPath(path)
		if !filepath.IsAbs(path) {
			wd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("get current directory: %w", err)
			}
			path = filepath.Join(wd, path)
		}
		return path, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	for {
		candidate := filepath.Join(wd, ".aexp.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	return "", fmt.Errorf("no .aexp.yaml found; run from a project directory or pass --config")
}

func setProjectConfigScalar(cfg *projectFileConfig, section, key, value string) error {
	switch section {
	case "":
		switch normalizeProjectKey(key) {
		case "resource":
			cfg.Resource = value
		case "cwd":
			cfg.Cwd = value
		case "env", "projectenv":
			cfg.Env = value
		case "condaenv":
			cfg.CondaEnv = value
		case "targetenv":
			cfg.TargetEnv = value
		case "defaultgpu", "gpu":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
			cfg.DefaultGPU = &n
		case "uievents":
			cfg.UIEvents = value
		default:
			cfg.Commands[key] = projectFileCommand{Command: value}
		}
	case "sync":
		switch normalizeProjectKey(key) {
		case "source":
			cfg.Sync.Source = value
		case "target":
			cfg.Sync.Target = value
		case "profile":
			cfg.Sync.Profile = value
		case "delete":
			cfg.Sync.DeleteExtra = parseProjectBool(value)
		case "nodefaultexcludes":
			cfg.Sync.NoDefaultExcludes = parseProjectBool(value)
		case "timeout":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
			cfg.Sync.TimeoutSec = n
		default:
			cfg.Warnings = append(cfg.Warnings, "unknown sync field ignored: "+key)
		}
	case "project":
		switch normalizeProjectKey(key) {
		case "id":
			cfg.Project.ID = value
		case "name":
			cfg.Project.Name = value
		case "vault":
			cfg.Project.Vault = value
		case "runcardindex", "runindex":
			cfg.Project.RunCardIndex = value
		case "proposaldir":
			cfg.Project.ProposalDir = value
		case "promotiondefault":
			cfg.Project.PromotionDefault = value
		default:
			cfg.Warnings = append(cfg.Warnings, "unknown project field ignored: "+key)
		}
	default:
		entry := ensureProjectCommand(cfg, section)
		switch normalizeProjectKey(key) {
		case "command", "cmd":
			entry.Command = value
		case "name":
			entry.Name = value
		case "kind":
			entry.Kind = value
		case "gpu", "gpuindex":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
			entry.GPUIndex = &n
		case "nogpu":
			entry.NoGPU = parseProjectBool(value)
		case "targetenv":
			entry.TargetEnv = value
		case "uievents":
			entry.UIEvents = value
		case "splitprotocol":
			entry.SplitProtocol = value
		case "evaluationprotocol", "evalprotocol":
			entry.EvaluationProtocol = value
		default:
			cfg.Warnings = append(cfg.Warnings, "unknown recipe field ignored: "+section+"."+key)
		}
		cfg.Commands[section] = entry
	}
	return nil
}

func addProjectConfigListValue(cfg *projectFileConfig, section, key, value string) {
	if value == "" {
		return
	}
	switch section {
	case "":
		switch normalizeProjectKey(key) {
		case "logs", "logpaths":
			cfg.Logs = append(cfg.Logs, value)
		case "metrics", "metricpaths":
			cfg.Metrics = append(cfg.Metrics, value)
		case "artifacts", "artifactpaths":
			cfg.Artifacts = append(cfg.Artifacts, value)
		default:
			cfg.Warnings = append(cfg.Warnings, "unknown project list ignored: "+key)
		}
	case "sync":
		if normalizeProjectKey(key) == "exclude" || normalizeProjectKey(key) == "excludes" {
			cfg.Sync.Excludes = append(cfg.Sync.Excludes, value)
		} else {
			cfg.Warnings = append(cfg.Warnings, "unknown sync list ignored: "+key)
		}
	case "project":
		cfg.Warnings = append(cfg.Warnings, "unknown project list ignored: "+key)
	default:
		entry := ensureProjectCommand(cfg, section)
		switch normalizeProjectKey(key) {
		case "logs", "logpaths":
			entry.Logs = append(entry.Logs, value)
		case "metrics", "metricpaths":
			entry.Metrics = append(entry.Metrics, value)
		case "artifacts", "artifactpaths":
			entry.Artifacts = append(entry.Artifacts, value)
		case "managedinputs", "runinputs":
			entry.Inputs = append(entry.Inputs, value)
		case "outputs", "runoutputs":
			entry.Outputs = append(entry.Outputs, value)
		default:
			cfg.Warnings = append(cfg.Warnings, "unknown recipe list ignored: "+section+"."+key)
		}
		cfg.Commands[section] = entry
	}
}

func ensureProjectCommand(cfg *projectFileConfig, name string) projectFileCommand {
	entry, ok := cfg.Commands[name]
	if !ok {
		entry = projectFileCommand{}
		cfg.Commands[name] = entry
	}
	return entry
}

func ensureProjectResourceProfile(cfg *projectFileConfig, name string) projectResourceProfile {
	if cfg.ResourceProfiles == nil {
		cfg.ResourceProfiles = map[string]projectResourceProfile{}
	}
	profile, ok := cfg.ResourceProfiles[name]
	if !ok {
		profile = projectResourceProfile{Name: name, EnvVars: map[string]string{}}
		cfg.ResourceProfiles[name] = profile
	}
	if profile.EnvVars == nil {
		profile.EnvVars = map[string]string{}
	}
	return profile
}

func setProjectResourceProfileScalar(cfg *projectFileConfig, profileName, key, value string) error {
	profile := ensureProjectResourceProfile(cfg, profileName)
	switch normalizeProjectKey(key) {
	case "resource":
		profile.Resource = value
	case "cwd":
		profile.Cwd = value
	case "env", "projectenv":
		profile.Env = value
	case "condaenv":
		profile.CondaEnv = value
	case "targetenv":
		profile.TargetEnv = value
	case "defaultgpu", "gpu":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		profile.DefaultGPU = &n
	case "uievents":
		profile.UIEvents = value
	default:
		cfg.Warnings = append(cfg.Warnings, "unknown resource profile field ignored: "+profileName+"."+key)
	}
	cfg.ResourceProfiles[profileName] = profile
	return nil
}

func setProjectResourceProfileEnv(cfg *projectFileConfig, profileName, key, value string) {
	profile := ensureProjectResourceProfile(cfg, profileName)
	profile.EnvVars[key] = value
	cfg.ResourceProfiles[profileName] = profile
}

func selectProjectResourceProfile(cfg *projectFileConfig, requestedResource string) (*projectResourceProfile, bool) {
	if cfg == nil || len(cfg.ResourceProfiles) == 0 {
		return nil, false
	}
	try := strings.TrimSpace(requestedResource)
	if try == "" {
		try = strings.TrimSpace(cfg.Resource)
	}
	if try != "" {
		if profile, ok := cfg.ResourceProfiles[try]; ok {
			return &profile, true
		}
		for _, profile := range cfg.ResourceProfiles {
			if strings.TrimSpace(profile.Resource) == try {
				return &profile, true
			}
		}
	}
	if cfg.Resource == "" && len(cfg.ResourceProfiles) == 1 {
		for _, profile := range cfg.ResourceProfiles {
			return &profile, true
		}
	}
	return nil, false
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stripProjectConfigComment(line string) string {
	inSingle := false
	inDouble := false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
				return strings.TrimRight(line[:i], " \t")
			}
		}
	}
	return strings.TrimRight(line, " \t")
}

func cleanProjectConfigValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func isProjectBlockScalar(value string) bool {
	return value == "|" || value == "|-" || value == "|+"
}

func collectProjectBlockScalar(lines []string, start int, parentIndent int) (string, int) {
	blockIndent := -1
	end := start
	for end < len(lines) {
		raw := lines[end]
		if strings.TrimSpace(raw) == "" {
			end++
			continue
		}
		indent := leadingSpaces(raw)
		if indent <= parentIndent {
			break
		}
		if blockIndent == -1 || indent < blockIndent {
			blockIndent = indent
		}
		end++
	}
	if blockIndent == -1 {
		return "", start - 1
	}
	out := make([]string, 0, end-start)
	for _, raw := range lines[start:end] {
		if strings.TrimSpace(raw) == "" {
			out = append(out, "")
			continue
		}
		if len(raw) >= blockIndent {
			out = append(out, raw[blockIndent:])
		} else {
			out = append(out, strings.TrimLeft(raw, " "))
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n"), end - 1
}

func leadingSpaces(s string) int {
	count := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func normalizeProjectKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, "-", "")
	return key
}

func parseProjectBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func mergeProjectLists(base, override []string) []string {
	if len(override) == 0 {
		return append([]string(nil), base...)
	}
	out := append([]string(nil), base...)
	return append(out, override...)
}

func printProjectConfigWarnings(cfg *projectFileConfig) {
	if cfg == nil || len(cfg.Warnings) == 0 {
		return
	}
	fmt.Println("Project config warnings")
	for _, warning := range cfg.Warnings {
		fmt.Printf("- %s\n", warning)
	}
	fmt.Println()
}

func printProjectRunPlan(configPath, commandName, resourceName string, req executor.SubmitRequest) {
	args := []string{
		"aexp", "run", "submit",
		"--resource", resourceName,
		"--kind", req.Kind,
		"--cwd", req.Cwd,
		"--project-env", req.ProjectEnv,
	}
	if req.Name != "" {
		args = append(args, "--name", req.Name)
	}
	if req.CondaEnv != "" {
		args = append(args, "--conda-env", req.CondaEnv)
	}
	if req.TargetEnv != "" {
		args = append(args, "--target-env", req.TargetEnv)
	}
	if req.RefreshProjectEnv {
		args = append(args, "--refresh-env")
	}
	if req.AllowEphemeralPaths {
		args = append(args, "--allow-ephemeral-paths")
	}
	if req.AllowDirtyGit {
		args = append(args, "--allow-dirty-git")
	}
	if req.RecordGitDiff {
		args = append(args, "--record-git-diff")
	}
	if req.Force {
		args = append(args, "--force", "--force-reason", req.ForceReason)
	}
	if req.PreemptRunID != "" {
		args = append(args, "--preempt-run", req.PreemptRunID, "--force-reason", req.ForceReason)
		if !req.PreemptSave {
			args = append(args, "--preempt-save=false")
		}
	}
	if req.GPUIndex == store.GPUIndexNone {
		args = append(args, "--no-gpu")
	} else {
		args = append(args, "--gpu-index", fmt.Sprintf("%d", req.GPUIndex))
	}
	for _, p := range req.LogPaths {
		args = append(args, "--log-paths", p)
	}
	for _, p := range req.MetricPaths {
		args = append(args, "--metric-paths", p)
	}
	for _, p := range req.ArtifactPaths {
		args = append(args, "--artifact-paths", p)
	}
	if req.UIEventsPath != "" {
		args = append(args, "--ui-events", req.UIEventsPath)
	}
	if req.ProjectConfigSHA256 != "" {
		args = append(args, "--config-sha256", req.ProjectConfigSHA256)
	}
	for _, dataset := range req.Datasets {
		args = append(args, "--dataset", dataset.DatasetID+"@"+dataset.Version)
	}
	for _, seed := range req.Seeds {
		args = append(args, "--seed", strconv.FormatInt(seed, 10))
	}
	if req.SplitProtocol != "" {
		args = append(args, "--split-protocol", req.SplitProtocol)
	}
	if req.EvaluationProtocol != "" {
		args = append(args, "--evaluation-protocol", req.EvaluationProtocol)
	}
	for _, input := range req.Inputs {
		spec, _ := json.Marshal(struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Revision string `json:"revision"`
			Mode     string `json:"mode"`
		}{
			From: input.LogicalURI, To: input.TargetPath, Revision: input.Revision, Mode: input.Mode,
		})
		args = append(args, "--input-json", string(spec))
	}
	for _, output := range req.Outputs {
		spec, _ := json.Marshal(struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Role     string `json:"role"`
			Required bool   `json:"required"`
		}{
			From: output.SourcePattern, To: output.LogicalURI, Role: output.Role, Required: output.Required,
		})
		args = append(args, "--output-json", string(spec))
	}
	args = append(args, "--shell", "--", req.Args[len(req.Args)-1])
	fmt.Println("Project run plan")
	fmt.Println()
	fmt.Printf("config:   %s\n", configPath)
	fmt.Printf("recipe:   %s\n", commandName)
	fmt.Printf("kind:     %s (%s)\n", req.Kind, projectKindEvidenceLabel(req.Kind))
	fmt.Printf("resource: %s\n", resourceName)
	fmt.Printf("cwd:      %s\n", req.Cwd)
	fmt.Printf("env:      %s\n", req.ProjectEnv)
	if req.CondaEnv != "" {
		fmt.Printf("conda:    %s\n", req.CondaEnv)
	}
	if req.TargetEnv != "" {
		fmt.Printf("target_env:%s\n", req.TargetEnv)
	}
	fmt.Printf("gpu:      %s\n", runGPULabelText(req.GPUIndex))
	if req.Force {
		fmt.Printf("force:    yes\n")
	}
	if req.ForceReason != "" {
		fmt.Printf("reason:   %s\n", req.ForceReason)
	}
	if req.PreemptRunID != "" {
		fmt.Printf("preempt:  %s (preserve=%v)\n", req.PreemptRunID, req.PreemptSave)
	}
	printStringList("logs", req.LogPaths)
	printStringList("metrics", req.MetricPaths)
	printStringList("artifacts", req.ArtifactPaths)
	if req.ProjectConfigSHA256 != "" {
		fmt.Printf("config_sha256: %s\n", req.ProjectConfigSHA256)
	}
	if len(req.Datasets) > 0 {
		fmt.Println("datasets:")
		for _, dataset := range req.Datasets {
			fmt.Printf("  - %s@%s (%s)\n", dataset.DatasetID, dataset.Version, dataset.ManifestSHA256)
		}
	}
	if len(req.Seeds) > 0 {
		values := make([]string, 0, len(req.Seeds))
		for _, seed := range req.Seeds {
			values = append(values, strconv.FormatInt(seed, 10))
		}
		fmt.Printf("seeds:    %s\n", strings.Join(values, ", "))
	}
	if len(req.Inputs) > 0 {
		fmt.Println("managed_inputs:")
		for _, input := range req.Inputs {
			fmt.Printf("  - %s -> %s (%s, %s)\n", input.LogicalURI, input.TargetPath, input.Revision, input.Mode)
		}
	}
	if len(req.Outputs) > 0 {
		fmt.Println("managed_outputs:")
		for _, output := range req.Outputs {
			fmt.Printf("  - %s -> %s (role=%s, required=%v)\n", output.SourcePattern, output.LogicalURI, output.Role, output.Required)
		}
	}
	if req.SplitProtocol != "" {
		fmt.Printf("split_protocol: %s\n", req.SplitProtocol)
	}
	if req.EvaluationProtocol != "" {
		fmt.Printf("evaluation_protocol: %s\n", req.EvaluationProtocol)
	}
	if req.UIEventsPath != "" {
		fmt.Printf("ui_events: %s\n", req.UIEventsPath)
	} else {
		fmt.Println("ui_events: .aexp/events/<run_id>.jsonl (default)")
	}
	if len(req.EnvVars) > 0 {
		keys := make([]string, 0, len(req.EnvVars))
		for key := range req.EnvVars {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Println("env_vars:")
		for _, key := range keys {
			fmt.Printf("  %s=%s\n", key, req.EnvVars[key])
		}
	}
	fmt.Println("command:")
	printIndentedBlock(req.Args[len(req.Args)-1], "  ")
	fmt.Println()
	fmt.Println("expanded submit command:")
	fmt.Println(joinShellArgs(args))
}

func projectKindEvidenceLabel(kind string) string {
	switch kind {
	case store.RunKindSetup:
		return "tooling only, not experiment evidence"
	case store.RunKindSmoke:
		return "smoke check, not experiment evidence"
	case store.RunKindPilot:
		return "pilot run"
	default:
		return "experiment evidence"
	}
}

func runGPULabelText(gpuIndex int) string {
	switch gpuIndex {
	case store.GPUIndexNone:
		return "none"
	case store.GPUIndexAll:
		return "all"
	default:
		return fmt.Sprintf("%d", gpuIndex)
	}
}

func printIndentedBlock(value string, prefix string) {
	if strings.TrimSpace(value) == "" {
		fmt.Println(prefix + "-")
		return
	}
	for _, line := range strings.Split(value, "\n") {
		fmt.Println(prefix + line)
	}
}

func submitConfiguredRun(ctx context.Context, resourceName string, submitReq executor.SubmitRequest, launchTimeoutSec int) error {
	db := openDB()
	defer db.Close()

	res, err := db.GetResourceByName(ctx, resourceName)
	if err != nil || res == nil {
		return fmt.Errorf("resource %s not found", resourceName)
	}
	submitReq.ResourceID = res.ID

	sshPool := executor.NewSSHPool(10 * time.Second)
	loadSSHKeys(sshPool)
	exec := executor.NewExecutor(sshPool, db)
	remoteFS := filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: sshPool}}
	fileService := filespace.NewService(db, remoteFS)
	planner := transfer.NewPlanner(db, fileService)
	transfers := transfer.NewService(db, planner)
	transport := transfer.NewRsyncTransport(db, remoteFS, transfer.SSHPoolTransferRunner{Pool: sshPool})
	worker := transfer.NewWorker(db, transport)
	exec.SetRunIO(runioservice.NewService(db, fileService, planner, transfers, worker, remoteFS))

	launchCtx := ctx
	var cancel context.CancelFunc
	if launchTimeoutSec > 0 {
		launchCtx, cancel = context.WithTimeout(ctx, time.Duration(launchTimeoutSec)*time.Second)
		defer cancel()
	}
	createdID := ""
	run, err := exec.SubmitVisibleWithOptions(launchCtx, submitReq, executor.SubmitOptions{
		OnCreated: func(run *store.Run) {
			createdID = run.ID
			fmt.Printf("Created run %s on %s\n", run.ID, resourceName)
			fmt.Printf("Logs:   aexp run logs %s --tail 100\n", run.ID)
			fmt.Printf("Status: aexp run status %s --short\n", run.ID)
			if run.UIEventsPath != "" {
				fmt.Printf("Events: %s\n", run.UIEventsPath)
			}
		},
	})
	if err != nil {
		if createdID != "" {
			return fmt.Errorf("launch failed for run %s: %w", createdID, err)
		}
		return err
	}
	fmt.Printf("Launched run %s on %s\n", run.ID, resourceName)
	printRunEventGuidance(run)
	printRunJournalGuidance(run)
	return nil
}

func resolveRunDatasetInputs(ctx context.Context, refs []string) ([]store.RunDatasetInput, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	db := openDB()
	defer db.Close()
	out := make([]store.RunDatasetInput, 0, len(refs))
	for _, ref := range refs {
		name, version, err := parseDatasetRef(ref)
		if err != nil {
			return nil, err
		}
		dataset, err := db.GetDatasetVersionByRef(ctx, name, version)
		if err != nil {
			return nil, err
		}
		if dataset == nil {
			return nil, fmt.Errorf("dataset %s is not registered", ref)
		}
		input, err := runDatasetInputFromVersion(dataset)
		if err != nil {
			return nil, err
		}
		out = append(out, input)
	}
	return out, nil
}

func runDatasetInputFromVersion(dataset *store.DatasetVersion) (store.RunDatasetInput, error) {
	if dataset == nil {
		return store.RunDatasetInput{}, fmt.Errorf("dataset is not registered")
	}
	ref := dataset.DatasetID + "@" + dataset.Version
	if dataset.State != store.DatasetStateVerified {
		return store.RunDatasetInput{}, fmt.Errorf("dataset %s is %s; formal evidence requires verified bytes (use aexp dataset ingest)", ref, dataset.State)
	}
	if strings.TrimSpace(dataset.ID) == "" || strings.TrimSpace(dataset.DatasetID) == "" || strings.TrimSpace(dataset.Version) == "" || strings.TrimSpace(dataset.ManifestSHA256) == "" {
		return store.RunDatasetInput{}, fmt.Errorf("dataset %s has incomplete immutable identity or manifest SHA-256", ref)
	}
	return store.RunDatasetInput{ID: dataset.ID, DatasetID: dataset.DatasetID, Version: dataset.Version, ManifestSHA256: dataset.ManifestSHA256}, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func printRunEventGuidance(run *store.Run) {
	if run == nil {
		return
	}
	if run.UIEventsPath == "" {
		if run.Kind == store.RunKindFormal || run.Kind == store.RunKindAblation {
			fmt.Println("Warning: this evidence run has structured events disabled; prefer leaving --ui-events at its default.")
		}
		return
	}
	fmt.Println("Structured events:")
	fmt.Printf("  AEXP_UI_EVENTS=%s\n", run.UIEventsPath)
	fmt.Println("  Instrument training/eval code before launch:")
	fmt.Println("    from aexp_events import metric, training_epoch, training_done, progress, param, note")
	fmt.Println("    metric(\"train/loss\", loss, epoch=epoch, trial=trial_id, variant=variant)")
	fmt.Println("    training_epoch(epoch, total=max_epochs, trial=trial_id, variant=variant)")
	fmt.Println("    training_done(epoch=last_epoch, total=max_epochs, best_epoch=best_epoch, early_stopped=early_stopped, trial=trial_id, variant=variant)")
	fmt.Println("  Do not reconstruct loss/metric/progress events after the run; write post-run interpretation in the Project journal.")
	fmt.Printf("  Monitor: aexp run snapshot %s --json\n", run.ID)
	fmt.Printf("  Events:  aexp run events %s --tail 50 --json\n", run.ID)
	fmt.Printf("  Metrics: aexp run metrics %s --latest --json\n", run.ID)
	fmt.Println("  Poll every 30-60s, then back off toward 120s if progress has not changed.")
}

func printRunJournalGuidance(run *store.Run) {
	if run == nil {
		return
	}
	if strings.TrimSpace(run.ProjectID) == "" {
		fmt.Println("After inspection: assign this Run to its canonical Project before recording research reasoning in the Project journal.")
		return
	}
	fmt.Printf("After inspection, append project reasoning with:\n  aexp project journal create %s --title \"...\" --body-md-file notes.md --run %s --next-action \"...\"\n", run.ProjectID, run.ID)
}

func runSyncPushFromProject(ctx context.Context, resourceName, source, target, profile string, excludes []string, dryRun, deleteExtra, noDefaultExcludes bool, timeoutSec int) error {
	res, exec, cleanup, err := syncResourceExecutor(resourceName)
	if err != nil {
		return err
	}
	defer cleanup()
	source = expandPath(source)
	target = resolveSyncRemotePath(res, target)
	resolvedExcludes, excludeSources, err := resolveSyncExcludes(source, profile, noDefaultExcludes, excludes)
	if err != nil {
		return err
	}
	useTarFallback := false
	var localRsyncErr, remoteRsyncErr error
	if !dryRun {
		_, localRsyncErr = osexec.LookPath("rsync")
		remoteRsyncErr = checkRemoteRsyncViaExec(ctx, exec, res, 20)
		if localRsyncErr != nil || remoteRsyncErr != nil {
			useTarFallback = true
		}
	}
	rsyncArgs := buildRsyncArgs(res, dryRun, deleteExtra, resolvedExcludes, nil)
	rsyncArgs = append(rsyncArgs, source, remoteRsyncSpec(res, target))
	if dryRun {
		printSyncDryRunExcludes(profile, resolvedExcludes, excludeSources)
		fmt.Println(joinShellArgs(append([]string{"rsync"}, rsyncArgs...)))
		return nil
	}
	if useTarFallback {
		if deleteExtra {
			return fmt.Errorf("remote rsync unavailable and tar fallback cannot preserve --delete semantics")
		}
		if _, err := osexec.LookPath("tar"); err != nil {
			return fmt.Errorf("rsync unavailable and local tar not found: %w", err)
		}
		if err := checkRemoteTarViaExec(ctx, exec, res, 20); err != nil {
			return fmt.Errorf("rsync unavailable and remote tar missing on %s: %w", res.Name, err)
		}
		if localRsyncErr != nil {
			fmt.Fprintf(os.Stderr, "warning: local rsync unavailable; falling back to ssh tar stream\n")
		}
		if remoteRsyncErr != nil {
			fmt.Fprintf(os.Stderr, "warning: remote rsync unavailable on %s; falling back to ssh tar stream\n", res.Name)
		}
		return runLocalTarPush(ctx, res, source, target, resolvedExcludes, timeoutSec)
	}
	if err := ensureRemoteDir(ctx, exec, res, target); err != nil {
		return err
	}
	return runLocalRsync(ctx, timeoutSec, rsyncArgs)
}

func projectDetectCmd() *cobra.Command {
	var resourceName, cwd, projectEnv, condaEnv string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Detect a project runtime profile and save it locally",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectEnv == "" {
				projectEnv = executor.ProjectEnvAuto
			}
			if projectEnv != executor.ProjectEnvAuto && projectEnv != executor.ProjectEnvRaw {
				return fmt.Errorf("--project-env must be raw or auto")
			}
			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}
			if cwd == "" {
				cwd = res.RootDir
			}
			if cwdEscapesRoot(res.RootDir, cwd) {
				return fmt.Errorf("cwd %q escapes resource root_dir %q", cwd, res.RootDir)
			}

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)
			exec := executor.NewExecutor(sshPool, db)
			profile, err := exec.DetectProject(cmd.Context(), res, cwd, projectEnv, condaEnv)
			if err != nil {
				return err
			}
			if err := db.SaveProjectProfile(cmd.Context(), profile); err != nil {
				return fmt.Errorf("save project profile: %w", err)
			}
			if asJSON {
				return printJSON(profile)
			}
			printProjectProfile(profile)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project working directory")
	cmd.Flags().StringVar(&projectEnv, "project-env", executor.ProjectEnvAuto, "Runtime env strategy: auto or raw")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment override for auto detection")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func projectDoctorCmd() *cobra.Command {
	var resourceName, cwd, condaEnv, configPath, recipeName string
	var gpuIndex int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate a project profile and print the next submit command",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgErr := loadProjectFileConfig(configPath)
			if cfgErr != nil && configPath != "" {
				return cfgErr
			}
			if cfg != nil {
				resourceFlagChanged := cmd.Flags().Changed("resource")
				cwdFlagChanged := cmd.Flags().Changed("cwd")
				condaEnvFlagChanged := cmd.Flags().Changed("conda-env")
				requestedResource := resourceName
				if !resourceFlagChanged && requestedResource == "" {
					requestedResource = cfg.Resource
				}
				resourceProfile, hasResourceProfile := selectProjectResourceProfile(cfg, requestedResource)
				if resourceName == "" {
					resourceName = cfg.Resource
				}
				if hasResourceProfile {
					if resourceProfile.Resource != "" {
						resourceName = resourceProfile.Resource
					} else if resourceName == "" {
						resourceName = resourceProfile.Name
					}
				}
				if !cwdFlagChanged && hasResourceProfile && resourceProfile.Cwd != "" {
					cwd = resourceProfile.Cwd
				}
				if cwd == "" {
					cwd = cfg.Cwd
				}
				if !condaEnvFlagChanged && hasResourceProfile && resourceProfile.CondaEnv != "" {
					condaEnv = resourceProfile.CondaEnv
				}
				if condaEnv == "" {
					condaEnv = cfg.CondaEnv
				}
				if !cmd.Flags().Changed("gpu-index") {
					if cfg.DefaultGPU != nil {
						gpuIndex = *cfg.DefaultGPU
					}
					if hasResourceProfile && resourceProfile.DefaultGPU != nil {
						gpuIndex = *resourceProfile.DefaultGPU
					}
					if recipeName != "" {
						if entry, ok := cfg.Commands[recipeName]; ok && entry.GPUIndex != nil {
							gpuIndex = *entry.GPUIndex
						}
					}
				}
			}
			if resourceName == "" {
				return fmt.Errorf("resource is required: set resource: in .aexp.yaml or pass --resource")
			}

			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}
			if cwd == "" {
				cwd = res.RootDir
			}
			if condaEnv == "" {
				condaEnv = res.CondaEnv
			}

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)
			report := runDoctorChecks(cmd.Context(), sshPool, res, cwd, condaEnv, gpuIndex)
			if err := updateResourceControlStatus(cmd.Context(), db, res, report); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to update resource ssh status: %v\n", err)
			}
			if cfg != nil {
				applyProjectDoctorConfigRecommendations(&report, cfg, recipeName)
			}
			if report.Project != nil {
				if err := db.SaveProjectProfile(cmd.Context(), report.Project); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to save project profile: %v\n", err)
				}
			}
			if asJSON {
				return printJSON(report)
			}
			printDoctorReport(report)
			if cfg != nil {
				printProjectDoctorRecipes(cfg, recipeName)
			}
			for _, check := range report.Checks {
				if !check.OK && check.Severity != "warn" {
					return fmt.Errorf("project doctor failed: %s", check.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project working directory")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment override for auto detection")
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path (default: nearest .aexp.yaml when present)")
	cmd.Flags().StringVar(&recipeName, "recipe", "", "Project recipe to validate in the recommendation")
	cmd.Flags().IntVar(&gpuIndex, "gpu-index", 0, "GPU index used in the recommended submit command")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func printProjectProfile(profile *store.ProjectProfile) {
	fmt.Printf("cwd: %s\n", profile.Cwd)
	fmt.Printf("env_strategy: %s\n", profile.EnvStrategy)
	fmt.Printf("resolved_env: %s\n", profile.ResolvedEnv)
	if profile.EnvName != "" {
		fmt.Printf("env_name: %s\n", profile.EnvName)
	}
	fmt.Printf("resolved_cwd: %s\n", profile.ResolvedCwd)
	if profile.Python != "" {
		fmt.Printf("python: %s\n", profile.Python)
	}
	fmt.Printf("torch: %s\n", boolWord(profile.TorchOK))
	fmt.Printf("cuda: %s\n", profile.CUDA)
	printStringList("entrypoints", profile.Entrypoints)
	printStringList("metrics", profile.Metrics)
	printStringList("logs", profile.Logs)
	printStringList("warnings", profile.Warnings)
}

func printStringList(label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, value := range values {
		fmt.Printf("  - %s\n", value)
	}
}

func boolWord(ok bool) string {
	if ok {
		return "ok"
	}
	return "unavailable"
}

// --- sync ---

type syncPlan struct {
	Resource      string   `json:"resource"`
	Source        string   `json:"source,omitempty"`
	Target        string   `json:"target,omitempty"`
	RemoteTarget  string   `json:"remote_target,omitempty"`
	LocalRsyncOK  bool     `json:"local_rsync_ok"`
	RemoteRsyncOK bool     `json:"remote_rsync_ok"`
	TargetOK      bool     `json:"target_ok"`
	WritableOK    bool     `json:"writable_ok"`
	Mode          string   `json:"mode"`
	Command       []string `json:"command,omitempty"`
	Recommended   []string `json:"recommended,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync project code/data with a resource using rsync",
		Long: `Sync code or data with a registered resource using rsync-style source/target arguments.

Typical flow:
  aexp sync doctor --resource mu ./ /remote/project/
  aexp sync push --resource mu ./ /remote/project/
  aexp sync pull --resource mu /remote/results/ ./results/

If the remote cannot be reached by local rsync but can reach another source,
use remote-pull:
  aexp sync remote-pull --resource mu ziwu@source:/path/project/ /remote/project/`,
	}
	cmd.AddCommand(syncDoctorCmd())
	cmd.AddCommand(syncPushCmd())
	cmd.AddCommand(syncPullCmd())
	cmd.AddCommand(syncRemotePullCmd())
	cmd.AddCommand(syncDatasetCmd())
	return cmd
}

func syncDoctorCmd() *cobra.Command {
	var resourceName string
	var asJSON bool
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "doctor [source] [target]",
		Short: "Check rsync availability and print recommended sync commands",
		Args:  cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, pool, cleanup, err := syncResourcePool(resourceName)
			if err != nil {
				return err
			}
			defer cleanup()

			source, target := "", ""
			if len(args) > 0 {
				source = args[0]
			}
			if len(args) > 1 {
				target = args[1]
			}
			plan := buildSyncPlan(cmd.Context(), pool, res, source, target, timeoutSec, !asJSON)
			if asJSON {
				return printJSON(plan)
			}
			printSyncPlan(plan)
			if !plan.LocalRsyncOK && !plan.RemoteRsyncOK {
				return fmt.Errorf("rsync unavailable locally and remotely")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 20, "Timeout per remote check in seconds")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func syncPushCmd() *cobra.Command {
	var resourceName string
	var dryRun, deleteExtra, noDefaultExcludes bool
	var profile string
	var excludes []string
	var extraArgs []string
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "push [flags] <local-source> <remote-target-dir>",
		Short: "Push local files to a resource with rsync or ssh tar fallback",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPushWithOptions(cmd.Context(), syncPushOptions{
				ResourceName:      resourceName,
				Source:            args[0],
				Target:            args[1],
				DryRun:            dryRun,
				DeleteExtra:       deleteExtra,
				NoDefaultExcludes: noDefaultExcludes,
				Profile:           profile,
				Excludes:          excludes,
				ExtraArgs:         extraArgs,
				TimeoutSec:        timeoutSec,
			})
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the rsync command without running it")
	cmd.Flags().BoolVar(&deleteExtra, "delete", false, "Delete files on target that no longer exist on source")
	cmd.Flags().StringVar(&profile, "profile", "code", "Exclude profile: code, code-data, all")
	cmd.Flags().BoolVar(&noDefaultExcludes, "no-default-excludes", false, "Disable profile excludes and .aexpignore; explicit --exclude still applies")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "Exclude pattern, repeatable")
	cmd.Flags().StringSliceVar(&extraArgs, "rsync-arg", nil, "Extra raw rsync argument, repeatable")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 0, "Timeout in seconds (0 = no timeout)")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

type syncPushOptions struct {
	ResourceName      string
	Source            string
	Target            string
	DryRun            bool
	DeleteExtra       bool
	NoDefaultExcludes bool
	Profile           string
	Excludes          []string
	ExtraArgs         []string
	TimeoutSec        int
	Retries           int
}

func runSyncPushWithOptions(ctx context.Context, opts syncPushOptions) error {
	res, exec, cleanup, err := syncResourceExecutor(opts.ResourceName)
	if err != nil {
		return err
	}
	defer cleanup()
	source := expandPath(opts.Source)
	target := resolveSyncRemotePath(res, opts.Target)
	resolvedExcludes, excludeSources, err := resolveSyncExcludes(source, opts.Profile, opts.NoDefaultExcludes, opts.Excludes)
	if err != nil {
		return err
	}
	useTarFallback := false
	var localRsyncErr, remoteRsyncErr error
	if !opts.DryRun {
		_, localRsyncErr = osexec.LookPath("rsync")
		remoteRsyncErr = checkRemoteRsyncViaExec(ctx, exec, res, 20)
		if localRsyncErr != nil || remoteRsyncErr != nil {
			useTarFallback = true
		}
	}
	rsyncArgs := buildRsyncArgs(res, opts.DryRun, opts.DeleteExtra, resolvedExcludes, opts.ExtraArgs)
	rsyncArgs = append(rsyncArgs, source, remoteRsyncSpec(res, target))
	if opts.DryRun {
		printSyncDryRunExcludes(opts.Profile, resolvedExcludes, excludeSources)
		fmt.Println(joinShellArgs(append([]string{"rsync"}, rsyncArgs...)))
		return nil
	}
	if useTarFallback {
		if opts.DeleteExtra {
			return fmt.Errorf("remote rsync unavailable and tar fallback cannot preserve --delete semantics")
		}
		if len(opts.ExtraArgs) > 0 {
			return fmt.Errorf("remote rsync unavailable and tar fallback cannot apply --rsync-arg")
		}
		if _, err := osexec.LookPath("tar"); err != nil {
			return fmt.Errorf("rsync unavailable and local tar not found: %w", err)
		}
		if err := checkRemoteTarViaExec(ctx, exec, res, 20); err != nil {
			return fmt.Errorf("rsync unavailable and remote tar missing on %s: %w", res.Name, err)
		}
		if localRsyncErr != nil {
			fmt.Fprintf(os.Stderr, "warning: local rsync unavailable; falling back to ssh tar stream\n")
		}
		if remoteRsyncErr != nil {
			fmt.Fprintf(os.Stderr, "warning: remote rsync unavailable on %s; falling back to ssh tar stream\n", res.Name)
		}
		return runLocalTarPush(ctx, res, source, target, resolvedExcludes, opts.TimeoutSec)
	}
	if err := ensureRemoteDir(ctx, exec, res, target); err != nil {
		return err
	}
	return runLocalRsyncWithRetries(ctx, opts.TimeoutSec, rsyncArgs, opts.Retries)
}

func runLocalRsyncWithRetries(ctx context.Context, timeoutSec int, args []string, retries int) error {
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "retrying rsync (%d/%d)...\n", attempt, retries)
		}
		if err := runLocalRsync(ctx, timeoutSec, args); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func syncPullCmd() *cobra.Command {
	var resourceName string
	var dryRun, deleteExtra bool
	var excludes []string
	var extraArgs []string
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "pull [flags] <remote-source> <local-target-dir>",
		Short: "Pull files from a resource with rsync",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, _, cleanup, err := syncResourceExecutor(resourceName)
			if err != nil {
				return err
			}
			defer cleanup()
			if !dryRun {
				if _, err := osexec.LookPath("rsync"); err != nil {
					return fmt.Errorf("local rsync not found: %w", err)
				}
			}
			source := resolveSyncRemotePath(res, args[0])
			target := expandPath(args[1])
			rsyncArgs := buildRsyncArgs(res, dryRun, deleteExtra, excludes, extraArgs)
			rsyncArgs = append(rsyncArgs, remoteRsyncSpec(res, source), target)
			if dryRun {
				fmt.Println(joinShellArgs(append([]string{"rsync"}, rsyncArgs...)))
				return nil
			}
			return runLocalRsync(cmd.Context(), timeoutSec, rsyncArgs)
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the rsync command without running it")
	cmd.Flags().BoolVar(&deleteExtra, "delete", false, "Delete files on target that no longer exist on source")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "Exclude pattern, repeatable")
	cmd.Flags().StringSliceVar(&extraArgs, "rsync-arg", nil, "Extra raw rsync argument, repeatable")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 0, "Timeout in seconds (0 = no timeout)")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func syncRemotePullCmd() *cobra.Command {
	var resourceName string
	var dryRun, deleteExtra, noDefaultExcludes bool
	var profile string
	var excludes []string
	var extraArgs []string
	var timeoutSec int

	cmd := &cobra.Command{
		Use:   "remote-pull [flags] <rsync-source> <remote-target-dir>",
		Short: "Run rsync on the remote resource so it pulls from another source",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, exec, cleanup, err := syncResourceExecutor(resourceName)
			if err != nil {
				return err
			}
			defer cleanup()
			target := resolveSyncRemotePath(res, args[1])
			resolvedExcludes, excludeSources, err := resolveSyncExcludes(".", profile, noDefaultExcludes, excludes)
			if err != nil {
				return err
			}
			if !dryRun {
				if err := ensureRemoteDir(cmd.Context(), exec, res, target); err != nil {
					return err
				}
			}
			remoteArgs := []string{"-avz", "--progress"}
			if dryRun {
				remoteArgs = append(remoteArgs, "--dry-run")
			}
			if deleteExtra {
				remoteArgs = append(remoteArgs, "--delete")
			}
			for _, pattern := range resolvedExcludes {
				remoteArgs = append(remoteArgs, "--exclude", pattern)
			}
			remoteArgs = append(remoteArgs, extraArgs...)
			remoteArgs = append(remoteArgs, args[0], target)
			remoteCmd := joinShellArgs(append([]string{"rsync"}, remoteArgs...))
			if dryRun {
				printSyncDryRunExcludes(profile, resolvedExcludes, excludeSources)
				fmt.Println(remoteCmd)
				return nil
			}
			req := executor.ExecRequest{
				ResourceID: res.ID,
				Command:    remoteCmd,
				TimeoutSec: timeoutSec,
				Actor:      "cli",
			}
			result, err := exec.Exec(cmd.Context(), req)
			if err != nil {
				return err
			}
			if result.Stdout != "" {
				fmt.Print(result.Stdout)
			}
			if result.Stderr != "" {
				fmt.Fprint(os.Stderr, result.Stderr)
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("remote rsync exit code %d", result.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the remote rsync command without running it")
	cmd.Flags().BoolVar(&deleteExtra, "delete", false, "Delete files on target that no longer exist on source")
	cmd.Flags().StringVar(&profile, "profile", "code", "Exclude profile: code, code-data, all")
	cmd.Flags().BoolVar(&noDefaultExcludes, "no-default-excludes", false, "Disable profile excludes and local .aexpignore; explicit --exclude still applies")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "Exclude pattern, repeatable")
	cmd.Flags().StringSliceVar(&extraArgs, "rsync-arg", nil, "Extra raw rsync argument, repeatable")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 0, "Timeout in seconds (0 = default exec timeout)")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

func syncDatasetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dataset",
		Short: "First-class dataset sync with data-safe defaults",
		Long: `Sync datasets with data-safe defaults.

Dataset sync keeps data files by default, uses resumable rsync partials, supports
checksum verification, and can retry transient transfer failures. It is intended
for durable dataset directories, not source-code-only project sync.`,
	}
	cmd.AddCommand(syncDatasetPushCmd())
	return cmd
}

func syncDatasetPushCmd() *cobra.Command {
	var resourceName string
	var dryRun, deleteExtra, noVerify bool
	var excludes []string
	var timeoutSec int
	var retries int

	cmd := &cobra.Command{
		Use:   "push [flags] <local-dataset-dir> <remote-dataset-dir>",
		Short: "Push a dataset directory with resumable, verifiable rsync defaults",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := expandPath(args[0])
			stats, statErr := localDatasetStats(source)
			if statErr != nil {
				return statErr
			}
			extraArgs := datasetSyncExtraArgs(!noVerify)
			if dryRun {
				fmt.Printf("dataset: %d files, %s\n", stats.Files, byteSize(stats.Bytes))
			}
			return runSyncPushWithOptions(cmd.Context(), syncPushOptions{
				ResourceName:      resourceName,
				Source:            source,
				Target:            args[1],
				DryRun:            dryRun,
				DeleteExtra:       deleteExtra,
				NoDefaultExcludes: false,
				Profile:           "code-data",
				Excludes:          excludes,
				ExtraArgs:         extraArgs,
				TimeoutSec:        timeoutSec,
				Retries:           retries,
			})
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the dataset rsync command without running it")
	cmd.Flags().BoolVar(&deleteExtra, "delete", false, "Delete remote dataset files not present locally")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "Disable rsync checksum verification")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "Exclude pattern, repeatable")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 0, "Timeout in seconds (0 = no timeout)")
	cmd.Flags().IntVar(&retries, "retries", 2, "Retry failed rsync transfers")
	_ = cmd.MarkFlagRequired("resource")
	return cmd
}

type datasetStats struct {
	Files int64
	Bytes int64
}

func localDatasetStats(source string) (datasetStats, error) {
	var stats datasetStats
	info, err := os.Stat(source)
	if err != nil {
		return stats, fmt.Errorf("dataset source not accessible: %w", err)
	}
	if !info.IsDir() {
		return stats, fmt.Errorf("dataset source must be a directory: %s", source)
	}
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stats.Files++
		stats.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("scan dataset source: %w", err)
	}
	return stats, nil
}

func datasetSyncExtraArgs(verify bool) []string {
	args := []string{
		"--partial",
		"--partial-dir=.aexp-rsync-partial",
		"--human-readable",
	}
	if verify {
		args = append(args, "--checksum")
	}
	return args
}

func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func syncResourcePool(resourceName string) (*store.Resource, *executor.SSHPool, func(), error) {
	db := openDB()
	cleanup := func() { db.Close() }
	res, err := db.GetResourceByName(context.Background(), resourceName)
	if err != nil || res == nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("resource %s not found", resourceName)
	}
	sshPool := executor.NewSSHPool(10 * time.Second)
	loadSSHKeys(sshPool)
	return res, sshPool, cleanup, nil
}

func syncResourceExecutor(resourceName string) (*store.Resource, *executor.Executor, func(), error) {
	db := openDB()
	cleanup := func() { db.Close() }
	res, err := db.GetResourceByName(context.Background(), resourceName)
	if err != nil || res == nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("resource %s not found", resourceName)
	}
	sshPool := executor.NewSSHPool(10 * time.Second)
	loadSSHKeys(sshPool)
	return res, executor.NewExecutor(sshPool, db), cleanup, nil
}

func buildSyncPlan(ctx context.Context, pool *executor.SSHPool, res *store.Resource, source, target string, timeoutSec int, progress bool) syncPlan {
	plan := syncPlan{
		Resource:     res.Name,
		Source:       source,
		Target:       target,
		RemoteTarget: resolveSyncRemotePath(res, target),
		Mode:         "push",
	}
	syncProgress(progress, "checking local rsync...")
	if _, err := osexec.LookPath("rsync"); err == nil {
		plan.LocalRsyncOK = true
		syncProgress(progress, "checking local rsync... ok")
	} else {
		plan.Warnings = append(plan.Warnings, "local rsync not found; use remote-pull if the resource can reach a source host")
		syncProgress(progress, "checking local rsync... missing")
	}
	if source != "" && !strings.Contains(source, ":") {
		syncProgress(progress, "checking local source...")
		if _, err := os.Stat(expandPath(source)); err != nil {
			plan.Warnings = append(plan.Warnings, "local source not accessible: "+err.Error())
			syncProgress(progress, "checking local source... unavailable")
		} else {
			syncProgress(progress, "checking local source... ok")
		}
	}
	syncProgress(progress, "checking remote rsync...")
	out, stderr, err := runSyncRemoteCheck(ctx, pool, res, "command -v rsync >/dev/null 2>&1 && rsync --version | head -1", timeoutSec)
	if err == nil {
		plan.RemoteRsyncOK = true
		detail := strings.TrimSpace(out)
		if detail == "" {
			detail = "ok"
		}
		syncProgress(progress, "checking remote rsync... "+detail)
	} else {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = err.Error()
		}
		plan.Warnings = append(plan.Warnings, "remote rsync unavailable: "+detail)
		syncProgress(progress, "checking remote rsync... unavailable")
	}
	if target != "" {
		syncProgress(progress, "checking remote target...")
		if err := checkRemoteTargetWritable(ctx, pool, res, plan.RemoteTarget, timeoutSec); err == nil {
			plan.TargetOK = true
			plan.WritableOK = true
			syncProgress(progress, "checking remote target... writable")
		} else {
			plan.Warnings = append(plan.Warnings, "remote target not writable: "+err.Error())
			syncProgress(progress, "checking remote target... not writable")
		}
	}
	if plan.LocalRsyncOK && target != "" && source != "" {
		args := buildRsyncArgs(res, true, false, nil, nil)
		args = append(args, source, remoteRsyncSpec(res, plan.RemoteTarget))
		plan.Command = append([]string{"rsync"}, args...)
		plan.Recommended = append(plan.Recommended, "aexp sync push --resource "+cliShellQuote(res.Name)+" "+cliShellQuote(source)+" "+cliShellQuote(target))
	}
	if plan.RemoteRsyncOK && target != "" {
		plan.Recommended = append(plan.Recommended, "aexp sync remote-pull --resource "+cliShellQuote(res.Name)+" <rsync-source> "+cliShellQuote(target))
	}
	return plan
}

func syncProgress(enabled bool, msg string) {
	if enabled {
		fmt.Fprintln(os.Stderr, msg)
	}
}

func printSyncPlan(plan syncPlan) {
	fmt.Println("AEXP Sync Doctor")
	fmt.Println()
	fmt.Printf("resource:     %s\n", plan.Resource)
	if plan.Source != "" {
		fmt.Printf("source:       %s\n", plan.Source)
	}
	if plan.Target != "" {
		fmt.Printf("target:       %s\n", plan.Target)
		fmt.Printf("remote_path:  %s\n", plan.RemoteTarget)
	}
	fmt.Printf("local_rsync:  %s\n", boolWord(plan.LocalRsyncOK))
	fmt.Printf("remote_rsync: %s\n", boolWord(plan.RemoteRsyncOK))
	if plan.Target != "" {
		fmt.Printf("target_write: %s\n", boolWord(plan.WritableOK))
	}
	if len(plan.Warnings) > 0 {
		fmt.Println()
		fmt.Println("warnings:")
		for _, warning := range plan.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
	if len(plan.Recommended) > 0 {
		fmt.Println()
		fmt.Println("recommended:")
		for _, command := range plan.Recommended {
			fmt.Println(command)
		}
	}
}

func checkRemoteTargetWritable(ctx context.Context, pool *executor.SSHPool, res *store.Resource, target string, timeoutSec int) error {
	if target == "" {
		return fmt.Errorf("remote target is required")
	}
	script := "target=" + cliShellQuote(target) + `; if [ -d "$target" ]; then test -w "$target"; else parent=$(dirname "$target"); test -d "$parent" && test -w "$parent"; fi`
	_, stderr, err := runSyncRemoteCheck(ctx, pool, res, script, timeoutSec)
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("target or parent directory is not writable: %s", detail)
	}
	return nil
}

func runSyncRemoteCheck(ctx context.Context, pool *executor.SSHPool, res *store.Resource, command string, timeoutSec int) (string, string, error) {
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	return pool.Exec(checkCtx, res.Host, res.Port, res.User, res.AuthRef, command, res.SocksProxy, res.ProxyCommand)
}

func checkRemoteRsyncViaExec(ctx context.Context, exec *executor.Executor, res *store.Resource, timeoutSec int) error {
	result, err := exec.Exec(ctx, executor.ExecRequest{
		ResourceID: res.ID,
		Command:    "command -v rsync >/dev/null 2>&1",
		TimeoutSec: timeoutSec,
		Actor:      "cli",
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = "rsync not found on remote"
		}
		return fmt.Errorf("%s", detail)
	}
	return nil
}

func checkRemoteTarViaExec(ctx context.Context, exec *executor.Executor, res *store.Resource, timeoutSec int) error {
	result, err := exec.Exec(ctx, executor.ExecRequest{
		ResourceID: res.ID,
		Command:    "command -v tar >/dev/null 2>&1",
		TimeoutSec: timeoutSec,
		Actor:      "cli",
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = "tar not found on remote"
		}
		return fmt.Errorf("%s", detail)
	}
	return nil
}

func ensureRemoteDir(ctx context.Context, exec *executor.Executor, res *store.Resource, target string) error {
	if target == "" {
		return fmt.Errorf("remote target is required")
	}
	result, err := exec.Exec(ctx, executor.ExecRequest{
		ResourceID: res.ID,
		Command:    "mkdir -p " + cliShellQuote(target) + " && test -w " + cliShellQuote(target),
		TimeoutSec: 20,
		Actor:      "cli",
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("mkdir/test failed: %s", strings.TrimSpace(result.Stderr))
	}
	return nil
}

func resolveSyncExcludes(source string, profile string, noDefault bool, explicit []string) ([]string, []string, error) {
	var excludes []string
	var sources []string
	if profile == "" {
		profile = "code"
	}
	profile = strings.ToLower(strings.TrimSpace(profile))

	if !noDefault {
		profileExcludes, err := syncProfileExcludes(profile)
		if err != nil {
			return nil, nil, err
		}
		if len(profileExcludes) > 0 {
			excludes = append(excludes, profileExcludes...)
			sources = append(sources, "profile:"+profile)
		}
		if profile != "all" && profile != "none" {
			if ignorePath := syncIgnorePath(source); ignorePath != "" {
				ignoreExcludes, err := readSyncIgnore(ignorePath)
				if err != nil {
					return nil, nil, err
				}
				if len(ignoreExcludes) > 0 {
					excludes = append(excludes, ignoreExcludes...)
					sources = append(sources, ignorePath)
				}
			}
		}
	}

	if len(explicit) > 0 {
		excludes = append(excludes, explicit...)
		sources = append(sources, "flags")
	}
	return dedupeStrings(excludes), sources, nil
}

func syncProfileExcludes(profile string) ([]string, error) {
	switch profile {
	case "all", "none":
		return nil, nil
	case "code":
		return append(append([]string(nil), defaultSyncExcludes...), codeOnlySyncExcludes...), nil
	case "code-data":
		return append([]string(nil), defaultSyncExcludes...), nil
	default:
		return nil, fmt.Errorf("unknown sync profile %q (expected code, code-data, or all)", profile)
	}
}

var defaultSyncExcludes = []string{
	".git/",
	".hg/",
	".svn/",
	".codegraph/",
	"node_modules/",
	".conda/",
	".venv/",
	"venv/",
	"env/",
	"ENV/",
	".mypy_cache/",
	".pytest_cache/",
	".ruff_cache/",
	".tox/",
	".nox/",
	"__pycache__/",
	"*.pyc",
	".ipynb_checkpoints/",
	".DS_Store",
	".aexp/",
	"logs/",
	"wandb/",
	"tensorboard/",
	"runs/detect/",
	"runs/train/",
	"runs/val/",
	"runs/predict/",
	"runs/**/weights/",
}

var codeOnlySyncExcludes = []string{
	"data/",
	"dataset/",
	"datasets/",
	"outputs/",
	"outputs_remote/",
	"output/",
	"results/",
	"runs/",
	"checkpoints/",
	"mlruns/",
	"lightning_logs/",
	"*.csv",
	"*.parquet",
	"*.npz",
	"*.pt",
	"*.pth",
	"*.safetensors",
	"*.ckpt",
	"*.zip",
	"*.tar",
	"*.tar.gz",
	"*.tgz",
}

func syncIgnorePath(source string) string {
	if source == "" || strings.Contains(source, ":") {
		return ""
	}
	path := expandPath(source)
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	ignorePath := filepath.Join(path, ".aexpignore")
	if _, err := os.Stat(ignorePath); err != nil {
		return ""
	}
	return ignorePath
}

func readSyncIgnore(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read .aexpignore: %w", err)
	}
	var excludes []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		excludes = append(excludes, line)
	}
	return excludes, nil
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func printSyncDryRunExcludes(profile string, excludes []string, sources []string) {
	if profile == "" {
		profile = "code"
	}
	fmt.Printf("# aexp sync profile: %s\n", profile)
	if len(sources) > 0 {
		fmt.Printf("# aexp sync exclude sources: %s\n", strings.Join(sources, ", "))
	}
	fmt.Println("# aexp sync excludes:")
	if len(excludes) == 0 {
		fmt.Println("#   (none)")
		return
	}
	for _, pattern := range excludes {
		fmt.Printf("#   %s\n", pattern)
	}
}

func buildRsyncArgs(res *store.Resource, dryRun bool, deleteExtra bool, excludes []string, extraArgs []string) []string {
	args := []string{"-avz", "--progress", "-e", rsyncSSHCommand(res)}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if deleteExtra {
		args = append(args, "--delete")
	}
	for _, pattern := range excludes {
		args = append(args, "--exclude", pattern)
	}
	args = append(args, extraArgs...)
	return args
}

func rsyncSSHCommand(res *store.Resource) string {
	return joinShellArgs(localSSHArgs(res))
}

func localSSHArgs(res *store.Resource) []string {
	parts := []string{
		"ssh",
		"-p", fmt.Sprintf("%d", res.Port),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
	}
	if res.AuthRef != "" {
		parts = append(parts, "-i", expandPath(res.AuthRef))
	}
	if res.ProxyCommand != "" {
		parts = append(parts, "-o", "ProxyCommand="+res.ProxyCommand)
	} else if res.SocksProxy != "" {
		parts = append(parts, "-o", "ProxyCommand=nc -X 5 -x "+res.SocksProxy+" %h %p")
	}
	return parts
}

func remoteRsyncSpec(res *store.Resource, remotePath string) string {
	return res.User + "@" + res.Host + ":" + remotePath
}

func resolveSyncRemotePath(res *store.Resource, target string) string {
	if target == "" {
		return res.RootDir
	}
	if strings.Contains(target, ":") {
		parts := strings.SplitN(target, ":", 2)
		target = parts[1]
	}
	if strings.HasPrefix(target, "/") {
		return target
	}
	return strings.TrimRight(res.RootDir, "/") + "/" + target
}

func runLocalRsync(ctx context.Context, timeoutSec int, args []string) error {
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}
	command := osexec.CommandContext(ctx, "rsync", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	if err := command.Run(); err != nil {
		return fmt.Errorf("rsync failed: %w", err)
	}
	return nil
}

func runLocalTarPush(ctx context.Context, res *store.Resource, source string, target string, excludes []string, timeoutSec int) error {
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}
	tarArgs, err := buildTarCreateArgs(source, excludes)
	if err != nil {
		return err
	}
	remoteCommand := executor.WithResourceRemotePath(res,
		"mkdir -p "+cliShellQuote(target)+" && tar -xzf - -C "+cliShellQuote(target))
	sshArgs := append(localSSHArgs(res), res.User+"@"+res.Host, remoteCommand)

	pr, pw := io.Pipe()
	defer pr.Close()

	tarCmd := osexec.CommandContext(ctx, "tar", tarArgs...)
	tarCmd.Stdout = pw
	tarCmd.Stderr = os.Stderr

	sshCmd := osexec.CommandContext(ctx, "ssh", sshArgs[1:]...)
	sshCmd.Stdin = pr
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr

	if err := sshCmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("start ssh tar extract: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		pw.Close()
		sshCmd.Process.Kill()
		sshCmd.Wait()
		return fmt.Errorf("start local tar: %w", err)
	}

	tarErr := tarCmd.Wait()
	_ = pw.CloseWithError(tarErr)
	sshErr := sshCmd.Wait()
	if tarErr != nil {
		return fmt.Errorf("local tar failed: %w", tarErr)
	}
	if sshErr != nil {
		return fmt.Errorf("remote tar extract failed: %w", sshErr)
	}
	return nil
}

func buildTarCreateArgs(source string, excludes []string) ([]string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat source: %w", err)
	}
	args := []string{"-czf", "-"}
	for _, pattern := range excludes {
		if strings.TrimSpace(pattern) != "" {
			args = append(args, "--exclude", pattern)
		}
	}
	if info.IsDir() {
		return append(args, "-C", source, "."), nil
	}
	return append(args, "-C", filepath.Dir(source), filepath.Base(source)), nil
}

func joinShellArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, cliShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// --- event ---

type eventOptions struct {
	path    string
	strict  bool
	fields  []string
	series  string
	run     string
	variant string
	split   string
	stage   string
	label   string
	trial   string
	seed    string
	fold    string
}

func eventCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "event",
		Aliases: []string{"events"},
		Short:   "Advanced/debug: append structured UI events for an aexp run",
		Hidden:  true,
		Long: `Advanced/debug compatibility command for appending structured JSONL events to $AEXP_UI_EVENTS.

This is not the normal agent workflow. Do not use this command after a run to
reconstruct training metrics, progress, or parameters. Loss curves, validation
metrics, progress, params, trial ids, and variant context should be emitted by
the training script while the experiment is executing via:

  from aexp_events import metric, training_epoch, training_done, progress, param, note

The command remains available for shell wrappers and legacy scripts running
inside an aexp run:

  aexp event metric train/loss 0.23 --epoch 3
  aexp event progress epoch 30 --total 100 --series iTransformer/raw --stage train
  aexp event metric val/loss 0.19 --epoch 3 --trial 7 --split val
  aexp event note "finished validation"

Keep metric names short and stable, such as train/loss, val/loss, or val/mse.
Put model, dataset, split, stage, and hyperparameter-trial context in fields
like --series, --variant, --split, --stage, and --trial. Do not encode a full
experiment config in the metric name; the UI uses these context fields to draw
multiple trials or variants in one chart.

For sweeps, --epoch is the trial-local training epoch. Use --trial/--variant for
global sweep identity instead of shifting the epoch axis.

The same command also works as aexp-event when the binary is symlinked with
that name.`,
	}
	cmd.AddCommand(eventMetricCmd())
	cmd.AddCommand(eventProgressCmd())
	cmd.AddCommand(eventParamCmd())
	cmd.AddCommand(eventNoteCmd())
	return cmd
}

func eventMetricCmd() *cobra.Command {
	var opts eventOptions
	var epoch, step string
	cmd := &cobra.Command{
		Use:   "metric <name> <value>",
		Short: "Emit a numeric metric event",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			value, err := parseEventNumber(args[1])
			if err != nil {
				return fmt.Errorf("metric value must be numeric: %w", err)
			}
			event, err := eventFromFields(opts)
			if err != nil {
				return err
			}
			event["type"] = "metric"
			event["name"] = args[0]
			event["value"] = value
			if epoch != "" {
				v, err := parseEventNumber(epoch)
				if err != nil {
					return fmt.Errorf("--epoch must be numeric: %w", err)
				}
				event["epoch"] = v
			}
			if step != "" {
				v, err := parseEventNumber(step)
				if err != nil {
					return fmt.Errorf("--step must be numeric: %w", err)
				}
				event["step"] = v
			}
			return emitStructuredEvent(opts, event)
		},
	}
	addEventFlags(cmd, &opts)
	cmd.Flags().StringVar(&epoch, "epoch", "", "Trial-local epoch value")
	cmd.Flags().StringVar(&step, "step", "", "Optional local/global step value")
	return cmd
}

func eventProgressCmd() *cobra.Command {
	var opts eventOptions
	var total string
	cmd := &cobra.Command{
		Use:   "progress <name> <current>",
		Short: "Emit a progress event",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			current, err := parseEventNumber(args[1])
			if err != nil {
				return fmt.Errorf("current must be numeric: %w", err)
			}
			event, err := eventFromFields(opts)
			if err != nil {
				return err
			}
			event["type"] = "progress"
			event["name"] = args[0]
			event["current"] = current
			if total != "" {
				v, err := parseEventNumber(total)
				if err != nil {
					return fmt.Errorf("--total must be numeric: %w", err)
				}
				event["total"] = v
			}
			return emitStructuredEvent(opts, event)
		},
	}
	addEventFlags(cmd, &opts)
	cmd.Flags().StringVar(&total, "total", "", "Total progress value")
	return cmd
}

func eventParamCmd() *cobra.Command {
	var opts eventOptions
	cmd := &cobra.Command{
		Use:   "param <name> <value>",
		Short: "Emit a run parameter event",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			event, err := eventFromFields(opts)
			if err != nil {
				return err
			}
			event["type"] = "param"
			event["name"] = args[0]
			event["value"] = parseEventValue(args[1])
			return emitStructuredEvent(opts, event)
		},
	}
	addEventFlags(cmd, &opts)
	return cmd
}

func eventNoteCmd() *cobra.Command {
	var opts eventOptions
	cmd := &cobra.Command{
		Use:   "note <text>",
		Short: "Emit a note event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			event, err := eventFromFields(opts)
			if err != nil {
				return err
			}
			event["type"] = "note"
			event["text"] = args[0]
			return emitStructuredEvent(opts, event)
		},
	}
	addEventFlags(cmd, &opts)
	return cmd
}

func addEventFlags(cmd *cobra.Command, opts *eventOptions) {
	cmd.Flags().StringVar(&opts.path, "path", "", "Event JSONL path (default: $AEXP_UI_EVENTS)")
	cmd.Flags().BoolVar(&opts.strict, "strict", false, "Fail if no event path is available")
	cmd.Flags().StringArrayVar(&opts.fields, "field", nil, "Extra field as key=value; may be repeated")
	cmd.Flags().StringVar(&opts.series, "series", "", "Series/context label for grouping related events")
	cmd.Flags().StringVar(&opts.run, "run", "", "Run or sub-run label for grouping related events")
	cmd.Flags().StringVar(&opts.variant, "variant", "", "Variant label, e.g. model or ablation name")
	cmd.Flags().StringVar(&opts.split, "split", "", "Data split label, e.g. train/val/test")
	cmd.Flags().StringVar(&opts.stage, "stage", "", "Stage label, e.g. setup/train/eval")
	cmd.Flags().StringVar(&opts.label, "label", "", "Human display label")
	cmd.Flags().StringVar(&opts.trial, "trial", "", "Trial id/label for hyperparameter search")
	cmd.Flags().StringVar(&opts.seed, "seed", "", "Seed id/label")
	cmd.Flags().StringVar(&opts.fold, "fold", "", "Fold id/label")
}

func eventFromFields(opts eventOptions) (map[string]interface{}, error) {
	event := make(map[string]interface{}, len(opts.fields)+10)
	for _, field := range opts.fields {
		key, value, ok := strings.Cut(field, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("--field must be key=value, got %q", field)
		}
		event[key] = parseEventValue(value)
	}
	setEventStringField(event, "series", opts.series)
	setEventStringField(event, "run", opts.run)
	setEventStringField(event, "variant", opts.variant)
	setEventStringField(event, "split", opts.split)
	setEventStringField(event, "stage", opts.stage)
	setEventStringField(event, "label", opts.label)
	setEventStringField(event, "trial", opts.trial)
	setEventStringField(event, "seed", opts.seed)
	setEventStringField(event, "fold", opts.fold)
	return event, nil
}

func setEventStringField(event map[string]interface{}, key, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		event[key] = value
	}
}

func emitStructuredEvent(opts eventOptions, event map[string]interface{}) error {
	warnings := structuredEventWarnings(event)
	normalizeStructuredEvent(event)
	warnings = append(warnings, structuredEventWarnings(event)...)
	if _, ok := event["time"]; !ok {
		event["time"] = float64(time.Now().UnixNano()) / 1e9
	}
	path := opts.path
	if path == "" {
		path = os.Getenv("AEXP_UI_EVENTS")
	}
	if path == "" {
		if opts.strict {
			return fmt.Errorf("AEXP_UI_EVENTS is not set; pass --path or run inside aexp run with UI events enabled")
		}
		fmt.Fprintln(os.Stderr, "warning: AEXP_UI_EVENTS is not set; event ignored")
		return nil
	}
	if path == "-" {
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(event); err != nil {
			return err
		}
		return encodeStructuredEventWarnings(enc, event, warnings)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create event directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open event file: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(event); err != nil {
		return err
	}
	return encodeStructuredEventWarnings(enc, event, warnings)
}

func normalizeStructuredEvent(event map[string]interface{}) {
	typ := strings.ToLower(asEventString(event["type"]))
	if typ != "metric" && typ != "metrics" && typ != "eval" && typ != "scalar" && typ != "progress" {
		return
	}
	if asEventString(event["series"]) != "" {
		if typ == "metric" || typ == "metrics" || typ == "eval" || typ == "scalar" {
			if name := asEventString(event["name"]); name != "" {
				event["name"] = normalizeMetricLeaf(name)
			}
		}
		return
	}
	name := asEventString(event["name"])
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return
	}
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	if len(clean) < 2 {
		return
	}
	leaf := clean[len(clean)-1]
	context := strings.Join(clean[:len(clean)-1], "/")
	if len(context) <= 20 && !strings.Contains(leaf, "_") {
		return
	}
	event["series"] = context
	if typ == "metric" || typ == "metrics" || typ == "eval" || typ == "scalar" {
		event["name"] = normalizeMetricLeaf(leaf)
	} else {
		event["name"] = leaf
	}
}

func normalizeMetricLeaf(name string) string {
	for _, split := range []string{"train", "val", "valid", "validation", "test", "eval"} {
		prefix := split + "_"
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			outSplit := split
			if outSplit == "valid" || outSplit == "validation" {
				outSplit = "val"
			}
			return outSplit + "/" + name[len(prefix):]
		}
	}
	return name
}

func encodeStructuredEventWarnings(enc *json.Encoder, event map[string]interface{}, warnings []EventQualityIssue) error {
	if strings.EqualFold(asEventString(event["type"]), "warning") {
		return nil
	}
	seen := map[string]bool{}
	for _, warning := range warnings {
		key := warning.Kind + "\x00" + warning.Name + "\x00" + warning.Series
		if warning.Kind == "" || seen[key] {
			continue
		}
		seen[key] = true
		ev := map[string]interface{}{
			"type":     "warning",
			"kind":     "event_quality",
			"issue":    warning.Kind,
			"severity": warning.Severity,
			"message":  warning.Message,
			"name":     warning.Name,
			"series":   warning.Series,
			"time":     event["time"],
		}
		if warning.Detail != nil {
			ev["detail"] = warning.Detail
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	return nil
}

func structuredEventWarnings(event map[string]interface{}) []EventQualityIssue {
	typ := strings.ToLower(asEventString(event["type"]))
	if typ == "warning" || typ == "note" || typ == "log" || typ == "message" {
		return nil
	}
	name := eventName(event)
	if name == "" {
		return nil
	}
	series := eventSeries(event)
	out := make([]EventQualityIssue, 0, 2)
	if isLongMetricIdentity(name, series) {
		out = append(out, EventQualityIssue{
			Severity: "warning",
			Kind:     "long_metric_name",
			Message:  "metric/progress identity looks too long; keep name semantic and put experiment identity in series/trial/variant fields",
			Name:     name,
			Series:   series,
			Detail: map[string]interface{}{
				"name_len":   len(name),
				"series_len": len(series),
			},
		})
	}
	if isMetricEvent(typ, event) && looksLikeParamName(name) {
		out = append(out, EventQualityIssue{
			Severity: "warning",
			Kind:     "constant_as_metric",
			Message:  "this looks like a parameter/config value emitted as a metric; use param(name, value) instead",
			Name:     name,
			Series:   series,
		})
	}
	return out
}

func parseEventNumber(value string) (float64, error) {
	return strconv.ParseFloat(value, 64)
}

func parseEventValue(value string) interface{} {
	switch strings.ToLower(value) {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseFloat(value, 64); err == nil {
		return n
	}
	return value
}

// --- init ---

func initCmd() *cobra.Command {
	var project bool
	var resourceName, cwd, envStrategy, condaEnv, outputPath string
	var defaultGPU int
	var force, dryRun, noEventsHelper bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize aexp (create config dir, generate SSH key)",
		RunE: func(cmd *cobra.Command, args []string) error {
			aexpDir := expandPath("~/.aexp")
			if err := os.MkdirAll(aexpDir, 0755); err != nil {
				return err
			}

			keyPath := filepath.Join(aexpDir, "id_ed25519")
			if _, err := os.Stat(keyPath); err == nil {
				fmt.Println("SSH key already exists at", keyPath)
			} else {
				fmt.Println("Generating SSH keypair at", keyPath)
				// Use ssh-keygen
				fmt.Println("Run: ssh-keygen -t ed25519 -f", keyPath, "-N ''")
				fmt.Println("Then copy the public key to your target resources.")
			}

			// Create empty DB
			dbPath := filepath.Join(aexpDir, "aexp.db")
			db, err := store.NewSQLite(dbPath)
			if err != nil {
				return fmt.Errorf("create database: %w", err)
			}
			db.Close()

			fmt.Println("Database created at", dbPath)
			fmt.Println("Ready. Run 'aexp serve' to start the server.")
			fmt.Println("Project config: run 'aexp project init' or 'aexp init --project' inside a repo to create .aexp.yaml.")
			if project {
				fmt.Println()
				fmt.Println("Creating project config...")
				return runProjectInit(projectInitOptions{
					Resource:       resourceName,
					Cwd:            cwd,
					Env:            envStrategy,
					CondaEnv:       condaEnv,
					OutputPath:     outputPath,
					DefaultGPU:     defaultGPU,
					Force:          force,
					DryRun:         dryRun,
					NoEventsHelper: noEventsHelper,
				})
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "Also create a project .aexp.yaml in the current directory")
	cmd.Flags().StringVar(&resourceName, "resource", "", "Project default resource name (with --project)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project remote working directory (with --project; default: current directory)")
	cmd.Flags().StringVar(&envStrategy, "env", executor.ProjectEnvAuto, "Project runtime env strategy: auto or raw (with --project)")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Project default conda environment (with --project)")
	cmd.Flags().IntVar(&defaultGPU, "default-gpu", 0, "Project default GPU index for formal recipes (with --project)")
	cmd.Flags().StringVar(&outputPath, "output", "", "Project config output path (with --project; default: .aexp.yaml)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing project config (with --project)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the project config without writing it (with --project)")
	cmd.Flags().BoolVar(&noEventsHelper, "no-events-helper", false, "Do not create project-local aexp_events.py (with --project)")
	return cmd
}

// --- resource ---

func resourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resource",
		Short: "Manage compute resources",
	}

	cmd.AddCommand(resourceListCmd())
	cmd.AddCommand(resourceAddCmd())
	cmd.AddCommand(resourceAddLocalCmd())
	cmd.AddCommand(resourceUpdateCmd())
	cmd.AddCommand(resourceRemoveCmd())
	cmd.AddCommand(resourceExploreCmd())

	return cmd
}

func resourceListCmd() *cobra.Command {
	var asJSON bool
	var verbose bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all resources with live status",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			resources, err := db.ListResources(cmd.Context())
			if err != nil {
				return err
			}

			if asJSON {
				return printJSON(resources)
			}

			if len(resources) == 0 {
				fmt.Println("No resources registered. Use 'aexp resource add' to add one.")
				return nil
			}

			if verbose {
				fmt.Printf("%-16s %-20s %-8s %-24s %-24s %-12s %-8s %-8s %-10s %-8s %-8s %s\n", "NAME", "HOST", "OS", "ROOT_DIR", "CONDA_BASE", "CONDA", "GPU", "STATUS", "SSH", "CPU", "RAM", "LAST_ERROR")
				for _, r := range resources {
					snap, _ := db.GetLatestSnapshot(cmd.Context(), r.ID)
					cpuStr := "-"
					ramStr := "-"
					if snap != nil {
						cpuStr = fmt.Sprintf("%.0f%%", snap.CPUPercent)
						if snap.MemTotalMB > 0 {
							ramStr = fmt.Sprintf("%.0f%%", snap.MemUsedMB/snap.MemTotalMB*100)
						}
					}
					fmt.Printf("%-16s %-20s %-8s %-24s %-24s %-12s %-8s %-8s %-10s %-8s %-8s %s\n",
						truncStr(r.Name, 16), truncStr(r.Host, 20), truncStr(r.OSType, 8), truncStr(r.RootDir, 24), truncStr(r.CondaBase, 24), truncStr(r.CondaEnv, 12), truncStr(r.GPUIndices, 8), r.Status, resourceControlLabel(r), cpuStr, ramStr, truncStr(r.LastDoctorError, 48))
				}
				return nil
			}

			fmt.Printf("%-16s %-20s %-8s %-10s %-8s %-8s  %s\n", "NAME", "HOST", "STATUS", "SSH", "CPU", "RAM", "GPU")
			for _, r := range resources {
				snap, _ := db.GetLatestSnapshot(cmd.Context(), r.ID)
				cpuStr := "-"
				ramStr := "-"
				gpuStr := "-"
				if snap != nil {
					cpuStr = fmt.Sprintf("%.0f%%", snap.CPUPercent)
					if snap.MemTotalMB > 0 {
						ramStr = fmt.Sprintf("%.0f%%", snap.MemUsedMB/snap.MemTotalMB*100)
					}
					if snap.GPUJSON != "" && snap.GPUJSON != "[]" {
						gpuStr = formatGPUList(snap.GPUJSON)
					}
				}
				fmt.Printf("%-16s %-20s %-8s %-10s %-8s %-8s  %s\n",
					truncStr(r.Name, 16), r.Host, r.Status, resourceControlLabel(r), cpuStr, ramStr, gpuStr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show root_dir, conda env, and configured GPU indices")
	return cmd
}

func resourceControlLabel(r store.Resource) string {
	switch r.SSHStatus {
	case store.ResourceSSHStatusOK:
		return "ssh:ok"
	case store.ResourceSSHStatusFailed:
		return "ssh:failed"
	default:
		return "ssh:?"
	}
}

// formatGPUList parses gpu_json and returns a compact summary like "4090 45% 1.2/24G"
func formatGPUList(gpuJSON string) string {
	var gpus []struct {
		Index    int     `json:"index"`
		Name     string  `json:"name"`
		Util     float64 `json:"util"`
		MemUsed  float64 `json:"mem_used"`
		MemTotal float64 `json:"mem_total"`
	}
	if err := json.Unmarshal([]byte(gpuJSON), &gpus); err != nil || len(gpus) == 0 {
		return "-"
	}
	var parts []string
	for _, g := range gpus {
		name := g.Name
		// Shorten GPU name: "NVIDIA GeForce RTX 4090" → "4090"
		for _, tok := range strings.Fields(name) {
			if len(tok) == 4 && tok[0] >= '0' && tok[0] <= '9' {
				name = tok
				break
			}
		}
		memUsed := fmt.Sprintf("%.0f", g.MemUsed/1024)
		memTotal := fmt.Sprintf("%.0f", g.MemTotal/1024)
		parts = append(parts, fmt.Sprintf("%s %d%% %s/%sG", name, int(g.Util), memUsed, memTotal))
	}
	return strings.Join(parts, " | ")
}

func resourceAddCmd() *cobra.Command {
	var name, host, osType, user, rootDir, remotePath, condaBase, condaInit, condaEnv, gpuIndices, tags, authRef, socksProxy, proxyCommand string
	var port int
	var resType string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a new resource",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || host == "" || rootDir == "" {
				return fmt.Errorf("--name, --host, --root-dir are required")
			}

			db := openDB()
			defer db.Close()

			r := &store.Resource{
				ID:           genID("rsrc_"),
				Name:         name,
				Type:         resType,
				Host:         host,
				OSType:       normalizeOSType(osType),
				Port:         port,
				User:         user,
				AuthRef:      authRef,
				RootDir:      rootDir,
				RemotePath:   remotePath,
				CondaBase:    condaBase,
				CondaInit:    condaInit,
				CondaEnv:     condaEnv,
				GPUIndices:   gpuIndices,
				Tags:         tags,
				SocksProxy:   socksProxy,
				ProxyCommand: proxyCommand,
				Status:       store.ResourceStatusUnknown,
			}

			if err := db.CreateResource(cmd.Context(), r); err != nil {
				return err
			}

			fmt.Printf("Registered resource %s (%s@%s:%d)\n", r.Name, r.User, r.Host, r.Port)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Resource name (required)")
	cmd.Flags().StringVar(&resType, "type", "ssh", "Resource type (ssh, docker, local)")
	cmd.Flags().StringVar(&host, "host", "", "SSH host (required)")
	cmd.Flags().StringVar(&osType, "os-type", "", "Operating system type (macos, linux)")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().StringVar(&user, "user", "root", "SSH user")
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "Workspace root directory (required)")
	cmd.Flags().StringVar(&remotePath, "remote-path", "", "PATH prefix for non-interactive remote commands (e.g. /opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin)")
	cmd.Flags().StringVar(&condaBase, "conda-base", "", "Conda/Miniforge base prefix (e.g. /home/user/miniforge3)")
	cmd.Flags().StringVar(&condaInit, "conda-init", "", "Conda init script path (defaults to <conda-base>/etc/profile.d/conda.sh)")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Default conda environment")
	cmd.Flags().StringVar(&gpuIndices, "gpu-indices", "", "Visible GPU indices (e.g. 0,1)")
	cmd.Flags().StringVar(&tags, "tags", "", "Comma-separated tags")
	cmd.Flags().StringVar(&authRef, "auth-ref", "", "SSH key path (default: ~/.aexp/id_ed25519)")
	cmd.Flags().StringVar(&socksProxy, "socks-proxy", "", "SOCKS5 proxy (host:port)")
	cmd.Flags().StringVar(&proxyCommand, "proxy-command", "", "SSH ProxyCommand (e.g. 'nc -X 5 -x host:port %h %p')")

	return cmd
}

func resourceAddLocalCmd() *cobra.Command {
	var name, rootDir string
	var skipTest bool

	cmd := &cobra.Command{
		Use:   "add-local",
		Short: "Register this machine as a localhost SSH resource",
		Long: `Register this machine as a normal SSH resource using 127.0.0.1.

This keeps execution unified: exec, run submit, logs, and artifacts all use
the same SSH-backed resource path. It does not enable SSH for you; on macOS,
turn on Remote Login first if localhost SSH is disabled.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := buildLocalSSHResource(name, rootDir)
			if err != nil {
				return err
			}

			if !skipTest {
				if err := testLocalSSH(cmd.Context(), r); err != nil {
					return err
				}
			}

			db := openDB()
			defer db.Close()
			if existing, _ := db.GetResourceByName(cmd.Context(), r.Name); existing != nil {
				return fmt.Errorf("resource %s already exists; use --name to choose another name", r.Name)
			}
			if err := db.CreateResource(cmd.Context(), r); err != nil {
				return err
			}
			fmt.Printf("Registered local resource %s (%s@%s:%d, os=%s, root=%s)\n",
				r.Name, r.User, r.Host, r.Port, r.OSType, r.RootDir)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Resource name (default: local-<hostname>)")
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "Workspace root directory (default: current directory)")
	cmd.Flags().BoolVar(&skipTest, "skip-test", false, "Register without testing ssh localhost first")
	return cmd
}

func resourceUpdateCmd() *cobra.Command {
	var name, host, osType, user, rootDir, remotePath, condaBase, condaInit, condaEnv, gpuIndices, tags, authRef, socksProxy, proxyCommand string
	var port int
	var resType string

	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Update an existing resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			r, err := db.GetResourceByName(cmd.Context(), args[0])
			if err != nil || r == nil {
				return fmt.Errorf("resource %s not found", args[0])
			}

			setString := func(flag string, dst *string, value string) {
				if cmd.Flags().Changed(flag) {
					*dst = value
				}
			}

			setString("name", &r.Name, name)
			setString("type", &r.Type, resType)
			setString("host", &r.Host, host)
			if cmd.Flags().Changed("os-type") {
				r.OSType = normalizeOSType(osType)
			}
			setString("user", &r.User, user)
			setString("auth-ref", &r.AuthRef, authRef)
			setString("root-dir", &r.RootDir, rootDir)
			setString("remote-path", &r.RemotePath, remotePath)
			setString("conda-base", &r.CondaBase, condaBase)
			setString("conda-init", &r.CondaInit, condaInit)
			setString("conda-env", &r.CondaEnv, condaEnv)
			setString("gpu-indices", &r.GPUIndices, gpuIndices)
			setString("tags", &r.Tags, tags)
			setString("socks-proxy", &r.SocksProxy, socksProxy)
			setString("proxy-command", &r.ProxyCommand, proxyCommand)
			if cmd.Flags().Changed("port") {
				r.Port = port
			}
			if r.Name == "" || r.Host == "" || r.RootDir == "" {
				return fmt.Errorf("name, host, and root_dir cannot be empty")
			}
			if r.Port == 0 {
				r.Port = 22
			}
			if r.User == "" {
				r.User = "root"
			}
			if r.Type == "" {
				r.Type = store.ResourceTypeSSH
			}

			if err := db.UpdateResource(cmd.Context(), r); err != nil {
				return err
			}

			fmt.Printf("Updated resource %s (%s@%s:%d)\n", r.Name, r.User, r.Host, r.Port)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New resource name")
	cmd.Flags().StringVar(&resType, "type", "", "Resource type (ssh, docker, local)")
	cmd.Flags().StringVar(&host, "host", "", "SSH host")
	cmd.Flags().StringVar(&osType, "os-type", "", "Operating system type (macos, linux)")
	cmd.Flags().IntVar(&port, "port", 0, "SSH port")
	cmd.Flags().StringVar(&user, "user", "", "SSH user")
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "Workspace root directory")
	cmd.Flags().StringVar(&remotePath, "remote-path", "", "PATH prefix for non-interactive remote commands")
	cmd.Flags().StringVar(&condaBase, "conda-base", "", "Conda/Miniforge base prefix (e.g. /home/user/miniforge3)")
	cmd.Flags().StringVar(&condaInit, "conda-init", "", "Conda init script path (defaults to <conda-base>/etc/profile.d/conda.sh)")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Default conda environment")
	cmd.Flags().StringVar(&gpuIndices, "gpu-indices", "", "Visible GPU indices (e.g. 0,1)")
	cmd.Flags().StringVar(&tags, "tags", "", "Comma-separated tags")
	cmd.Flags().StringVar(&authRef, "auth-ref", "", "SSH key path")
	cmd.Flags().StringVar(&socksProxy, "socks-proxy", "", "SOCKS5 proxy (host:port)")
	cmd.Flags().StringVar(&proxyCommand, "proxy-command", "", "SSH ProxyCommand (e.g. 'nc -X 5 -x host:port %h %p')")

	return cmd
}

func resourceRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove [name]",
		Short: "Remove a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			r, err := db.GetResourceByName(cmd.Context(), args[0])
			if err != nil || r == nil {
				return fmt.Errorf("resource %s not found", args[0])
			}

			if err := db.DeleteResource(cmd.Context(), r.ID); err != nil {
				return err
			}
			fmt.Printf("Removed resource %s\n", r.Name)
			return nil
		},
	}
}

func resourceExploreCmd() *cobra.Command {
	var user string
	var port int
	var keyPath string
	var socksProxy string
	var proxyCommand string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "explore [host]",
		Short: "Discover environment on a remote host (GPU, conda, workspace, etc.)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := args[0]

			sshPool := executor.NewSSHPool(10 * time.Second)
			defaultKeyPath := loadSSHKeys(sshPool)
			if keyPath != "" {
				keyPath = expandPath(keyPath)
				if err := sshPool.AddKey(keyPath); err != nil {
					return err
				}
			} else {
				keyPath = defaultKeyPath
			}

			fmt.Fprintf(os.Stderr, "Exploring %s@%s:%d ...\n", user, host, port)

			d, err := explore.Explore(cmd.Context(), sshPool, host, port, user, keyPath, socksProxy, proxyCommand)
			if err != nil {
				return err
			}

			if asJSON {
				return printJSON(d)
			}

			fmt.Print(explore.FormatDiscovery(d))
			return nil
		},
	}

	cmd.Flags().StringVar(&user, "user", "root", "SSH user")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().StringVar(&keyPath, "key", "", "SSH private key path (overrides default loaded keys)")
	cmd.Flags().StringVar(&keyPath, "auth-ref", "", "Alias for --key")
	cmd.Flags().StringVar(&socksProxy, "socks-proxy", "", "SOCKS5 proxy (host:port)")
	cmd.Flags().StringVar(&proxyCommand, "proxy-command", "", "SSH ProxyCommand (e.g. 'nc -X 5 -x host:port %h %p')")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

// --- run ---

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Manage experiment runs",
		Long: `Manage experiment runs.

Runs are immutable execution records: command, environment, telemetry, metrics,
logs, and artifacts. Research interpretation belongs to the Project journal.
After inspecting a Project Run, append reasoning with:

  aexp project journal create <project_id> --title ... --body-md-file notes.md --run <run_id> --next-action ...

Historical "run mark" commands remain callable only for compatibility and are
hidden from the normal command surface.`,
	}

	cmd.AddCommand(runSubmitCmd())
	cmd.AddCommand(runListCmd())
	cmd.AddCommand(runStatusCmd())
	cmd.AddCommand(runRefreshCmd())
	cmd.AddCommand(runLogsCmd())
	cmd.AddCommand(runEventsCmd())
	cmd.AddCommand(runMetricsCmd())
	cmd.AddCommand(runEventQualityCmd())
	cmd.AddCommand(runSnapshotCmd())
	cmd.AddCommand(runCancelCmd())
	cmd.AddCommand(runFreezeCmd())
	cmd.AddCommand(runArchiveCmd())
	cmd.AddCommand(runRestoreCmd())
	cmd.AddCommand(runDeleteCmd())
	legacyMark := runMarkCmd()
	legacyMark.Hidden = true
	legacyMarks := runMarksCmd()
	legacyMarks.Hidden = true
	cmd.AddCommand(legacyMark)
	cmd.AddCommand(legacyMarks)

	return cmd
}

func runFreezeCmd() *cobra.Command {
	var profileName, destination, workspace, configPath string
	var dryRun, asJSON, wait bool
	cmd := &cobra.Command{Use: "freeze <run_id>", Short: "Freeze paper evidence to authoritative storage", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadProjectFileConfig(configPath)
		if err != nil {
			return err
		}
		profile, ok := cfg.FreezeProfiles[profileName]
		if !ok {
			return fmt.Errorf("freeze profile %q not found in %s", profileName, cfg.Path)
		}
		if destination == "" {
			destination = "storage://" + profile.Storage + "/" + strings.Trim(profile.StoragePrefix, "/")
		}
		if workspace != "" {
			workspace = expandPath(workspace)
			if !filepath.IsAbs(workspace) {
				workspace = filepath.Join(filepath.Dir(cfg.Path), workspace)
			}
		}
		db := openDB()
		defer db.Close()
		plan, err := freezer.BuildPlan(cmd.Context(), db, args[0], profile, destination, workspace)
		if err != nil {
			return err
		}
		if dryRun {
			if asJSON {
				return printJSON(plan)
			}
			printFreezePlan(plan)
			return nil
		}
		if !plan.Eligible {
			if asJSON {
				_ = printJSON(plan)
			}
			return fmt.Errorf("freeze plan blocked by %d requirement(s)", len(plan.Blockers))
		}
		if workspace == "" {
			return fmt.Errorf("--workspace is required for aggregate and release gate")
		}
		record, err := freezer.NewRecord(plan)
		if err != nil {
			return err
		}
		existing, err := db.GetRunFreeze(cmd.Context(), record.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			if err := db.CreateRunFreeze(cmd.Context(), record); err != nil {
				return err
			}
		} else {
			record = existing
		}
		if record.State == store.RunFreezeQueued || record.State == store.RunFreezeFailed {
			if wait {
				if err := runFreezeWorker(cmd.Context(), record.ID); err != nil {
					return err
				}
			} else if err := startFreezeWorker(record.ID); err != nil {
				return err
			}
		}
		if wait {
			record, _ = db.GetRunFreeze(cmd.Context(), record.ID)
		}
		if asJSON {
			return printJSON(record)
		}
		fmt.Printf("Freeze %s: %s (%s)\n", record.ID, record.State, record.DestinationURI)
		if !wait {
			fmt.Printf("Status: aexp freeze status %s --json\n", record.ID)
		}
		return nil
	}}
	cmd.Flags().StringVar(&profileName, "profile", "paper", "Freeze profile from .aexp.yaml")
	cmd.Flags().StringVar(&destination, "to", "", "Canonical storage:// destination")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Paper evidence workspace projection")
	cmd.Flags().StringVar(&configPath, "config", "", "Project config path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Plan and validate without writing or transferring")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	cmd.Flags().BoolVar(&wait, "wait", false, "Run in the foreground and wait for completion")
	return cmd
}

func printFreezePlan(plan *freezer.Plan) {
	fmt.Printf("Freeze plan %s eligible=%v\n", plan.FreezeID, plan.Eligible)
	fmt.Printf("Destination: %s\nFiles: %d  Bytes: %s\n", plan.DestinationURI, plan.FileCount, byteSize(plan.TotalBytes))
	for _, b := range plan.Blockers {
		fmt.Printf("BLOCKED %-28s %s\n", b.Code, b.Message)
	}
}

func freezeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "freeze", Short: "Deprecated compatibility commands for legacy evidence freezes; use snapshot"}
	cmd.AddCommand(freezeStatusCmd(), freezeListCmd(), freezeManifestCmd(), freezeMaterializeCmd(), freezeWorkerCmd())
	return cmd
}

func snapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Create and inspect immutable references to verified Run outputs",
		Long:  "Evidence Snapshots reference final RunManifest and already-published output revisions. They never discover or transfer files.",
	}
	cmd.AddCommand(snapshotCreateCmd(), snapshotGetCmd(), snapshotListCmd())
	return cmd
}

func snapshotCreateCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create <run_id>",
		Short: "Create an idempotent Evidence Snapshot for one Run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			snapshot, created, err := db.CreateEvidenceSnapshot(cmd.Context(), args[0])
			if err != nil {
				if blocked, ok := err.(*store.EvidenceSnapshotBlockedError); ok && asJSON {
					return printJSON(map[string]interface{}{"error": "SNAPSHOT_BLOCKED", "blockers": blocked.Blockers})
				}
				return err
			}
			if asJSON {
				return printJSON(map[string]interface{}{"snapshot": snapshot, "created": created})
			}
			fmt.Printf("%s\t%s\t%s\n", snapshot.ID, snapshot.RunID, snapshot.ManifestSHA256)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func snapshotGetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get <snapshot_id>",
		Short: "Inspect an Evidence Snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			snapshot, err := db.GetEvidenceSnapshot(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if snapshot == nil {
				return fmt.Errorf("snapshot %s not found", args[0])
			}
			if asJSON {
				return printJSON(snapshot)
			}
			fmt.Printf("%s\t%s\t%s\t%s\n", snapshot.ID, snapshot.RunID, snapshot.ProjectID, snapshot.ManifestSHA256)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func snapshotListCmd() *cobra.Command {
	var runID string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Evidence Snapshots for one Run",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(runID) == "" {
				return fmt.Errorf("--run is required")
			}
			db := openDB()
			defer db.Close()
			items, err := db.ListEvidenceSnapshots(cmd.Context(), runID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(items)
			}
			for _, snapshot := range items {
				fmt.Printf("%s\t%s\t%s\n", snapshot.ID, snapshot.ProjectID, snapshot.ManifestSHA256)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "Run id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func releaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Evaluate a Snapshot with the Project's single aggregate and gate commands",
		Long:  "Every evaluation appends an immutable Release event. Gate rejection records blocked; operational command failure records failed; the Snapshot is never modified.",
	}
	cmd.AddCommand(releaseEvaluateCmd(), releaseGetCmd(), releaseListCmd())
	return cmd
}

func releaseEvaluateCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "evaluate <snapshot_id>",
		Short: "Run the configured aggregate and release gate, then append a Release event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			result, err := (releaseservice.Service{Store: db}).Evaluate(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s\t%s\t%s\t%d\n", result.ID, result.SnapshotID, result.State, result.Sequence)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func releaseGetCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get <release_id>",
		Short: "Inspect one immutable Release event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			result, err := db.GetEvidenceRelease(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if result == nil {
				return fmt.Errorf("release %s not found", args[0])
			}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("%s\t%s\t%s\t%d\n", result.ID, result.SnapshotID, result.State, result.Sequence)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func releaseListCmd() *cobra.Command {
	var snapshotID string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List immutable Release events for one Snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(snapshotID) == "" {
				return fmt.Errorf("--snapshot is required")
			}
			db := openDB()
			defer db.Close()
			items, err := db.ListEvidenceReleases(cmd.Context(), snapshotID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(items)
			}
			for _, result := range items {
				fmt.Printf("%s\t%s\t%d\n", result.ID, result.State, result.Sequence)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&snapshotID, "snapshot", "", "Snapshot id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}

func freezeMaterializeCmd() *cobra.Command {
	var destination, expectedPlan, initiator string
	var wait, asJSON bool
	cmd := &cobra.Command{Use: "materialize <freeze_id>", Aliases: []string{"restore"}, Short: "Restore frozen raw evidence through the shared TransferJob engine", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		services, cleanup := openFileSpaceCLI()
		defer cleanup()
		freeze, err := services.db.GetRunFreeze(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if freeze == nil {
			return fmt.Errorf("freeze %s not found", args[0])
		}
		source, revision, err := freezeRestoreSource(cmd.Context(), services.db, freeze)
		if err != nil {
			return err
		}
		request := transfer.PlanRequest{Source: source, Destination: destination, SourceRevision: revision, Initiator: initiator, Verification: "manifest"}
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
			transport := transfer.NewRsyncTransport(services.db, remote, transfer.SSHPoolTransferRunner{Pool: services.pool})
			worker := transfer.NewWorker(services.db, transport)
			if err := worker.Execute(cmd.Context(), job.ID); err != nil {
				return err
			}
			job, err = services.db.GetTransferJob(cmd.Context(), job.ID)
			if err != nil {
				return err
			}
		}
		if asJSON {
			return printJSON(map[string]any{"freeze_id": freeze.ID, "source": source, "transfer": job, "created": created})
		}
		fmt.Printf("%s\t%s\t%s\n", freeze.ID, job.ID, job.State)
		return nil
	}}
	cmd.Flags().StringVar(&destination, "to", "", "Destination aexp://, resource://, storage://, or local:// URI (required)")
	cmd.Flags().StringVar(&expectedPlan, "plan-sha256", "", "Expected plan hash; omitted only for an atomic CLI plan+start")
	cmd.Flags().StringVar(&initiator, "initiator", "auto", "auto, nas, compute, or mac")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for destination verification")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func freezeRestoreSource(ctx context.Context, db store.Store, freeze *store.RunFreeze) (string, string, error) {
	if freeze == nil || freeze.RawTransferID == "" || freeze.FrozenAt == nil {
		return "", "", fmt.Errorf("freeze raw evidence is not durably frozen")
	}
	job, err := db.GetTransferJob(ctx, freeze.RawTransferID)
	if err != nil || job == nil {
		if err == nil {
			err = fmt.Errorf("raw transfer %s not found", freeze.RawTransferID)
		}
		return "", "", err
	}
	if job.State != store.TransferCompleted {
		return "", "", fmt.Errorf("raw transfer %s is %s, not completed", job.ID, job.State)
	}
	record, err := db.GetTransferPlan(ctx, job.PlanSHA256)
	if err != nil || record == nil {
		if err == nil {
			err = fmt.Errorf("raw transfer plan %s not found", job.PlanSHA256)
		}
		return "", "", err
	}
	plan, err := transfer.DecodePlan(record)
	if err != nil {
		return "", "", err
	}
	if plan.Destination.URI == "" || plan.Source.Revision == "" {
		return "", "", fmt.Errorf("raw transfer plan lacks a durable destination or pinned revision")
	}
	return plan.Destination.URI, plan.Source.Revision, nil
}
func freezeStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "status <freeze_id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		f, err := db.GetRunFreeze(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("freeze %s not found", args[0])
		}
		if asJSON {
			return printJSON(f)
		}
		fmt.Printf("%s %s/%s %d/%d files %s\n", f.ID, f.State, f.Stage, f.FilesDone, f.FileCount, f.LastError)
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}
func freezeListCmd() *cobra.Command {
	var runID string
	var asJSON bool
	cmd := &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		items, err := db.ListRunFreezes(cmd.Context(), runID)
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(items)
		}
		for _, f := range items {
			fmt.Printf("%-24s %-14s %-16s %s\n", f.ID, f.State, f.RunID, f.DestinationURI)
		}
		return nil
	}}
	cmd.Flags().StringVar(&runID, "run", "", "Filter by run ID")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return cmd
}
func freezeManifestCmd() *cobra.Command {
	return &cobra.Command{Use: "manifest <freeze_id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		db := openDB()
		defer db.Close()
		f, err := db.GetRunFreeze(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("freeze %s not found", args[0])
		}
		files, err := db.ListRunFreezeFiles(cmd.Context(), f.ID)
		if err != nil {
			return err
		}
		return printJSON(map[string]interface{}{"freeze": f, "files": files})
	}}
}
func freezeWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "worker <freeze_id>", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runFreezeWorker(cmd.Context(), args[0]) }}
	return cmd
}
func runFreezeWorker(ctx context.Context, id string) error {
	db := openDB()
	defer db.Close()
	pool := executor.NewSSHPool(15 * time.Second)
	loadSSHKeys(pool)
	defer pool.CloseAll()
	remote := filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: pool}}
	fileService := filespace.NewService(db, remote)
	planner := transfer.NewPlanner(db, fileService)
	transfers := transfer.NewService(db, planner)
	transport := transfer.NewRsyncTransport(db, remote, transfer.SSHPoolTransferRunner{Pool: pool})
	worker := transfer.NewWorker(db, transport)
	return freezer.ExecuteManaged(ctx, freezer.ManagedRuntime{Store: db, Planner: planner, Transfers: transfers, Worker: worker, Writer: remote}, id)
}
func startFreezeWorker(id string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := expandPath("~/.aexp/freeze-worker.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	cmd := osexec.Command(exe, "freeze", "worker", id)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		return err
	}
	return nil
}

func runSubmitCmd() *cobra.Command {
	var resource, name, cwd, condaEnv, projectEnv, targetEnv, kind, uiEventsPath, forceReason, preemptRunID, configSHA256, splitProtocol, evaluationProtocol string
	var gpuIndex int
	var shellMode, force, noGPU, refreshEnv, preemptSave, allowDirtyGit, recordGitDiff bool
	var logPaths, artifactPaths, metricPaths []string
	var datasetRefs []string
	var inputSpecs, outputSpecs, inputJSONSpecs, outputJSONSpecs []string
	var seeds []int64
	var launchTimeoutSec int
	var allowEphemeralPaths bool

	cmd := &cobra.Command{
		Use:   "submit [flags] -- <program> [args...]",
		Short: "Submit a new experiment run",
		Long: `Submit an experiment run. Two modes:

aexp is a local dispatcher: the remote host only needs SSH, tmux, and your runtime.
The --cwd flag is constrained to the resource root_dir. If your project lives
elsewhere, register the resource with that root_dir first.

  Structured (default): argv preserved exactly
    aexp run submit --resource mu -- python train.py --lr 0.001

  Shell mode (--shell): full shell interpretation
    aexp run submit --resource mu --shell -- 'echo start; python train.py | tee log'

  Setup task (tracked, async, but not experiment evidence; defaults to no GPU):
    aexp run submit --resource mu --kind setup --cwd /workspace/project --shell -- 'python -m pip install -r requirements.txt'

  Structured UI events (generic JSONL dashboard; defaults to .aexp/events/<run_id>.jsonl):
    aexp run submit --resource mu -- python train.py
    python can import: from aexp_events import emit, metric, progress, param, note`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var submitReq executor.SubmitRequest
			if noGPU || (kind == store.RunKindSetup && !cmd.Flags().Changed("gpu-index")) {
				gpuIndex = store.GPUIndexNone
			}

			if shellMode {
				// Shell mode: wrap as bash -lc "<command>"
				submitReq = executor.SubmitRequest{
					Program: "bash",
					Args:    []string{"-lc", joinArgs(args)},
				}
			} else {
				// Structured mode: program + argv
				submitReq = executor.SubmitRequest{
					Program: args[0],
					Args:    args[1:],
				}
			}

			submitReq.ResourceID = resource
			submitReq.Name = name
			submitReq.Kind = kind
			submitReq.GPUIndex = gpuIndex
			submitReq.Force = force
			submitReq.ForceReason = forceReason
			submitReq.PreemptRunID = preemptRunID
			submitReq.PreemptSave = preemptSave
			submitReq.Cwd = cwd
			submitReq.CondaEnv = condaEnv
			submitReq.ProjectEnv = projectEnv
			submitReq.TargetEnv = targetEnv
			submitReq.LogPaths = logPaths
			submitReq.ArtifactPaths = artifactPaths
			submitReq.MetricPaths = metricPaths
			submitReq.UIEventsPath = uiEventsPath
			submitReq.RefreshProjectEnv = refreshEnv
			submitReq.AllowEphemeralPaths = allowEphemeralPaths
			submitReq.AllowDirtyGit = allowDirtyGit
			submitReq.RecordGitDiff = recordGitDiff
			submitReq.ProjectConfigSHA256 = strings.TrimSpace(configSHA256)
			submitReq.Seeds = append([]int64(nil), seeds...)
			submitReq.SplitProtocol = strings.TrimSpace(splitProtocol)
			submitReq.EvaluationProtocol = strings.TrimSpace(evaluationProtocol)
			for _, spec := range inputSpecs {
				binding, err := parseRunInputSpec(spec)
				if err != nil {
					return err
				}
				submitReq.Inputs = append(submitReq.Inputs, binding)
			}
			for _, spec := range inputJSONSpecs {
				binding, err := parseRunInputJSON(spec)
				if err != nil {
					return err
				}
				submitReq.Inputs = append(submitReq.Inputs, binding)
			}
			for _, spec := range outputSpecs {
				binding, err := parseRunOutputSpec(spec)
				if err != nil {
					return err
				}
				submitReq.Outputs = append(submitReq.Outputs, binding)
			}
			for _, spec := range outputJSONSpecs {
				binding, err := parseRunOutputJSON(spec)
				if err != nil {
					return err
				}
				submitReq.Outputs = append(submitReq.Outputs, binding)
			}

			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resource)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resource)
			}
			submitReq.ResourceID = res.ID
			for _, ref := range datasetRefs {
				datasetName, version, err := parseDatasetRef(ref)
				if err != nil {
					return err
				}
				dataset, err := db.GetDatasetVersionByRef(cmd.Context(), datasetName, version)
				if err != nil {
					return err
				}
				if dataset == nil {
					return fmt.Errorf("dataset %s is not registered", ref)
				}
				input, err := runDatasetInputFromVersion(dataset)
				if err != nil {
					return err
				}
				submitReq.Datasets = append(submitReq.Datasets, input)
			}

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)

			exec := executor.NewExecutor(sshPool, db)
			remoteFS := filespace.PythonRemoteFS{Runner: filespace.SSHPoolRunner{Pool: sshPool}}
			fileService := filespace.NewService(db, remoteFS)
			planner := transfer.NewPlanner(db, fileService)
			transfers := transfer.NewService(db, planner)
			transport := transfer.NewRsyncTransport(db, remoteFS, transfer.SSHPoolTransferRunner{Pool: sshPool})
			worker := transfer.NewWorker(db, transport)
			exec.SetRunIO(runioservice.NewService(db, fileService, planner, transfers, worker, remoteFS))

			launchCtx := cmd.Context()
			var cancel context.CancelFunc
			if launchTimeoutSec > 0 {
				launchCtx, cancel = context.WithTimeout(cmd.Context(), time.Duration(launchTimeoutSec)*time.Second)
				defer cancel()
			}
			createdID := ""
			run, err := exec.SubmitVisibleWithOptions(launchCtx, submitReq, executor.SubmitOptions{
				OnCreated: func(run *store.Run) {
					createdID = run.ID
					fmt.Printf("Created run %s on %s\n", run.ID, resource)
					fmt.Printf("Logs:   aexp run logs %s --tail 100\n", run.ID)
					fmt.Printf("Status: aexp run status %s --short\n", run.ID)
					if run.UIEventsPath != "" {
						fmt.Printf("Events: %s\n", run.UIEventsPath)
					}
				},
			})
			if err != nil {
				if createdID != "" {
					return fmt.Errorf("launch failed for run %s: %w", createdID, err)
				}
				return err
			}

			fmt.Printf("Launched run %s on %s\n", run.ID, resource)
			printRunEventGuidance(run)
			printRunJournalGuidance(run)
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Run name")
	cmd.Flags().StringVar(&kind, "kind", "formal", "Run kind: setup, smoke, pilot, formal, ablation")
	cmd.Flags().IntVar(&gpuIndex, "gpu-index", store.GPUIndexAll, "GPU index to use (-1 for all)")
	cmd.Flags().BoolVar(&noGPU, "no-gpu", false, "Do not reserve GPUs or set CUDA_VISIBLE_DEVICES")
	cmd.Flags().BoolVar(&shellMode, "shell", false, "Shell mode: interpret command via bash -lc")
	cmd.Flags().BoolVar(&force, "force", false, "Skip GPU slot lock, allow concurrent runs on same resource/GPU; requires --force-reason")
	cmd.Flags().StringVar(&forceReason, "force-reason", "", "Required with --force or --preempt-run; records why GPU safety was overridden")
	cmd.Flags().StringVar(&preemptRunID, "preempt-run", "", "Cancel this active run before submitting the new run")
	cmd.Flags().BoolVar(&preemptSave, "preempt-save", true, "Record that the preempted run should be preserved for evidence review")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment")
	cmd.Flags().StringVar(&projectEnv, "project-env", "", "Runtime env strategy: auto or raw")
	cmd.Flags().StringVar(&targetEnv, "target-env", "", "Intended target environment for setup/repair runs (records semantics; does not wrap the command)")
	cmd.Flags().BoolVar(&refreshEnv, "refresh-env", false, "Ignore cached project profile and re-detect the environment")
	cmd.Flags().StringSliceVar(&logPaths, "log-paths", nil, "Log file globs")
	cmd.Flags().StringSliceVar(&artifactPaths, "artifact-paths", nil, "Artifact file globs")
	cmd.Flags().StringSliceVar(&metricPaths, "metric-paths", nil, "Metric file globs")
	cmd.Flags().StringSliceVar(&datasetRefs, "dataset", nil, "Registered dataset reference name@version (repeatable)")
	cmd.Flags().Int64SliceVar(&seeds, "seed", nil, "Declared experiment seed (repeatable)")
	cmd.Flags().StringVar(&configSHA256, "config-sha256", "", "Launch-time project/config SHA-256")
	cmd.Flags().StringVar(&splitProtocol, "split-protocol", "", "Pinned data split/evaluation split protocol")
	cmd.Flags().StringVar(&evaluationProtocol, "evaluation-protocol", "", "Pinned metric/evaluation protocol")
	cmd.Flags().StringSliceVar(&inputSpecs, "input", nil, "Managed input URI|target|revision[|mode] (repeatable)")
	cmd.Flags().StringArrayVar(&inputJSONSpecs, "input-json", nil, "Managed input JSON object with from/to/revision/mode (repeatable)")
	cmd.Flags().StringSliceVar(&outputSpecs, "output", nil, "Managed output pattern|URI|role|required (repeatable)")
	cmd.Flags().StringArrayVar(&outputJSONSpecs, "output-json", nil, "Managed output JSON object with from/to/role/required (repeatable)")
	cmd.Flags().StringVar(&uiEventsPath, "ui-events", "", "Structured UI event JSONL file (default .aexp/events/<run_id>.jsonl; set off to disable)")
	cmd.Flags().BoolVar(&allowEphemeralPaths, "allow-ephemeral-paths", false, "Allow cwd/root_dir that look like temporary mounts; use only for disposable smoke/setup runs")
	cmd.Flags().BoolVar(&allowDirtyGit, "allow-dirty-git", false, "Allow a formal/ablation run from a dirty Git worktree")
	cmd.Flags().BoolVar(&recordGitDiff, "record-git-diff", false, "When allowing dirty Git, save a local patch under ~/.aexp/git-diffs")
	cmd.Flags().BoolVar(&allowDirtyGit, "allow-dirty", false, "Alias for --allow-dirty-git")
	cmd.Flags().BoolVar(&recordGitDiff, "record-diff", false, "Alias for --record-git-diff")
	_ = cmd.Flags().MarkHidden("allow-dirty")
	_ = cmd.Flags().MarkHidden("record-diff")
	cmd.Flags().IntVar(&launchTimeoutSec, "launch-timeout", 60, "Timeout in seconds for remote launch after the run record is created (0 = no timeout)")

	return cmd
}

func parseRunInputSpec(spec string) (store.RunInputBinding, error) {
	parts := strings.Split(spec, "|")
	if len(parts) < 3 || len(parts) > 4 {
		return store.RunInputBinding{}, fmt.Errorf("input %q must be URI|target|revision[|mode]", spec)
	}
	binding := store.RunInputBinding{LogicalURI: strings.TrimSpace(parts[0]), TargetPath: strings.TrimSpace(parts[1]), Revision: strings.TrimSpace(parts[2]), Mode: "copy"}
	if len(parts) == 4 && strings.TrimSpace(parts[3]) != "" {
		binding.Mode = strings.TrimSpace(parts[3])
	}
	if binding.LogicalURI == "" || binding.TargetPath == "" || binding.Revision == "" {
		return store.RunInputBinding{}, fmt.Errorf("input URI, target and revision are required")
	}
	return binding, nil
}

func parseRunOutputSpec(spec string) (store.RunOutputBinding, error) {
	parts := strings.Split(spec, "|")
	if len(parts) != 4 {
		return store.RunOutputBinding{}, fmt.Errorf("output %q must be pattern|URI|role|required", spec)
	}
	required, err := strconv.ParseBool(strings.TrimSpace(parts[3]))
	if err != nil {
		return store.RunOutputBinding{}, fmt.Errorf("output required must be true or false: %w", err)
	}
	binding := store.RunOutputBinding{SourcePattern: strings.TrimSpace(parts[0]), LogicalURI: strings.TrimSpace(parts[1]), Role: strings.TrimSpace(parts[2]), Required: required}
	if binding.SourcePattern == "" || binding.LogicalURI == "" {
		return store.RunOutputBinding{}, fmt.Errorf("output pattern and URI are required")
	}
	return binding, nil
}

func parseRunInputJSON(spec string) (store.RunInputBinding, error) {
	var value struct {
		From, URI, LogicalURI  string
		To, Target, TargetPath string
		Revision, Mode         string
	}
	if err := json.Unmarshal([]byte(spec), &value); err != nil {
		return store.RunInputBinding{}, fmt.Errorf("invalid input JSON: %w", err)
	}
	binding := store.RunInputBinding{LogicalURI: firstNonEmpty(value.From, value.URI, value.LogicalURI), TargetPath: firstNonEmpty(value.To, value.Target, value.TargetPath), Revision: value.Revision, Mode: value.Mode}
	if binding.Mode == "" {
		binding.Mode = "copy"
	}
	if binding.LogicalURI == "" || binding.TargetPath == "" || binding.Revision == "" {
		return store.RunInputBinding{}, fmt.Errorf("input JSON requires from, to, and revision")
	}
	return binding, nil
}

func parseRunOutputJSON(spec string) (store.RunOutputBinding, error) {
	var value struct {
		From, Pattern, SourcePattern string
		To, URI, LogicalURI          string
		Role                         string
		Required                     bool
	}
	if err := json.Unmarshal([]byte(spec), &value); err != nil {
		return store.RunOutputBinding{}, fmt.Errorf("invalid output JSON: %w", err)
	}
	binding := store.RunOutputBinding{SourcePattern: firstNonEmpty(value.From, value.Pattern, value.SourcePattern), LogicalURI: firstNonEmpty(value.To, value.URI, value.LogicalURI), Role: value.Role, Required: value.Required}
	if binding.SourcePattern == "" || binding.LogicalURI == "" {
		return store.RunOutputBinding{}, fmt.Errorf("output JSON requires from and to")
	}
	return binding, nil
}

func runListCmd() *cobra.Command {
	var status, resource, project string
	var asJSON bool
	var summary bool
	var noRefresh bool
	var trash, deleted, importantOnly bool
	var refreshTimeoutSec int
	var limit, offset int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			filter := store.RunFilter{Status: status, ProjectID: project, Limit: limit, Offset: offset, Trash: trash, Deleted: deleted, ImportantOnly: importantOnly}
			if resource != "" {
				res, _ := db.GetResourceByName(cmd.Context(), resource)
				if res != nil {
					filter.ResourceID = res.ID
				}
			}

			if summary {
				items, err := db.ListRunSummaries(cmd.Context(), filter)
				if err != nil {
					return err
				}
				return printJSON(items)
			}
			runs, err := db.ListRuns(cmd.Context(), filter)
			if err != nil {
				return err
			}

			cached := map[string]bool{}
			if !noRefresh {
				runs, cached = refreshActiveRuns(cmd.Context(), db, runs, refreshTimeoutSec)
				if status != "" {
					filtered := runs[:0]
					for _, r := range runs {
						if r.Status == status {
							filtered = append(filtered, r)
						}
					}
					runs = filtered
				}
			}

			if asJSON {
				return printJSON(runs)
			}

			fmt.Printf("%-15s %-25s %-20s %-10s %-12s %-8s %s\n", "RUN_ID", "NAME", "RESOURCE", "KIND", "STATUS", "GPU", "COMMAND")
			for _, r := range runs {
				name := r.Name
				if name == "" {
					name = "-"
				}
				displayStatus := r.Status
				if cached[r.ID] {
					displayStatus += " (cached)"
				}
				fmt.Printf("%-15s %-25s %-20s %-10s %-12s %-8s %s\n",
					r.ID, truncStr(name, 25), r.ResourceID, r.Kind, displayStatus, displayRunGPU(r.GPUIndex), truncStr(r.Command, 60))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource name")
	cmd.Flags().StringVar(&project, "project", "", "Filter by project id")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum runs to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "Pagination offset")
	cmd.Flags().BoolVar(&summary, "summary", false, "Return low-noise run summaries as JSON")
	cmd.Flags().BoolVar(&trash, "trash", false, "List runs in trash")
	cmd.Flags().BoolVar(&deleted, "deleted", false, "List logically deleted runs")
	cmd.Flags().BoolVar(&importantOnly, "important", false, "Only runs marked important by a project run card")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&noRefresh, "no-refresh", false, "Do not refresh running/starting runs before listing")
	cmd.Flags().IntVar(&refreshTimeoutSec, "refresh-timeout", 5, "Timeout per running/starting status refresh in seconds")

	return cmd
}

func refreshActiveRuns(ctx context.Context, db store.Store, runs []store.Run, timeoutSec int) ([]store.Run, map[string]bool) {
	sshPool := executor.NewSSHPool(10 * time.Second)
	loadSSHKeys(sshPool)
	exec := executor.NewExecutor(sshPool, db)
	refreshed, cached, _ := exec.RefreshRuns(ctx, runs, time.Duration(timeoutSec)*time.Second)
	return refreshed, cached
}

func runStatusCmd() *cobra.Command {
	var asJSON bool
	var short bool

	cmd := &cobra.Command{
		Use:   "status [run_id]",
		Short: "Show run status (auto-refreshes running runs from remote)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil || run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}

			// Auto-refresh running/starting runs from remote
			if store.IsRunRefreshableStatus(run.Status) {
				sshPool := executor.NewSSHPool(10 * time.Second)
				loadSSHKeys(sshPool)
				exec := executor.NewExecutor(sshPool, db)
				refreshed, err := exec.CheckRunStatus(cmd.Context(), run.ID)
				if err == nil && refreshed != nil {
					run = refreshed
				}
			}

			if asJSON {
				if short {
					return printJSON(shortRunStatus(db, cmd.Context(), run))
				}
				return printJSON(run)
			}

			if short {
				printShortRunStatus(db, cmd.Context(), run)
				return nil
			}

			fmt.Printf("ID:        %s\n", run.ID)
			fmt.Printf("Name:      %s\n", run.Name)
			fmt.Printf("Resource:  %s\n", run.ResourceID)
			fmt.Printf("Status:    %s\n", run.Status)
			fmt.Printf("Kind:      %s\n", run.Kind)
			fmt.Printf("Command:   %s\n", run.Command)
			fmt.Printf("CWD:       %s\n", run.Cwd)
			if run.ProjectEnv != "" {
				fmt.Printf("ProjectEnv:%s\n", run.ProjectEnv)
			}
			if run.TargetEnv != "" {
				fmt.Printf("TargetEnv: %s\n", run.TargetEnv)
			}
			if run.ForceReason != "" {
				fmt.Printf("Force:     %s\n", run.ForceReason)
			}
			if run.PreemptRunID != "" {
				fmt.Printf("Preempted: %s (preserve=%v)\n", run.PreemptRunID, run.PreemptSave)
			}
			if run.ResolvedEnv != "" {
				fmt.Printf("Resolved:  %s\n", run.ResolvedEnv)
			}
			if run.ResolvedPython != "" {
				fmt.Printf("Python:    %s\n", run.ResolvedPython)
			}
			if run.ResolvedCwd != "" {
				fmt.Printf("Run CWD:   %s\n", run.ResolvedCwd)
			}
			fmt.Printf("tmux:      %s\n", run.TmuxSession)
			if run.GPUIndex >= 0 {
				fmt.Printf("GPU:       %d\n", run.GPUIndex)
			} else if run.GPUIndex == store.GPUIndexNone {
				fmt.Printf("GPU:       none\n")
			}
			if run.ExitCode.Valid {
				fmt.Printf("Exit code: %d\n", run.ExitCode.Int64)
			}
			if run.StartedAt.Valid {
				fmt.Printf("Started:   %s\n", run.StartedAt.Time.Format(time.RFC3339))
			}
			if run.FinishedAt.Valid {
				fmt.Printf("Finished:  %s\n", run.FinishedAt.Time.Format(time.RFC3339))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&short, "short", false, "Show compact status and output paths")
	return cmd
}

func runRefreshCmd() *cobra.Command {
	var resourceName string
	var timeoutSec int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "refresh [run_id]",
		Short: "Refresh running/starting run status from remote",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)
			exec := executor.NewExecutor(sshPool, db)

			if len(args) == 1 {
				run, err := exec.CheckRunStatus(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(run)
				}
				fmt.Printf("%s  %s  gpu=%s\n", run.ID, run.Status, displayRunGPU(run.GPUIndex))
				return nil
			}

			resourceID := ""
			if resourceName != "" {
				res, err := db.GetResourceByName(cmd.Context(), resourceName)
				if err != nil || res == nil {
					return fmt.Errorf("resource %s not found", resourceName)
				}
				resourceID = res.ID
			}
			runs, cached, err := exec.RefreshActiveRuns(cmd.Context(), resourceID, time.Duration(timeoutSec)*time.Second)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(runs)
			}
			fmt.Printf("%-15s %-20s %-10s %-8s\n", "RUN_ID", "RESOURCE", "STATUS", "GPU")
			for _, run := range runs {
				status := run.Status
				if cached[run.ID] {
					status += " (cached)"
				}
				fmt.Printf("%-15s %-20s %-10s %-8s\n", run.ID, run.ResourceID, status, displayRunGPU(run.GPUIndex))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceName, "resource", "", "Refresh active runs for a resource")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 5, "Timeout per running/starting status refresh in seconds")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func shortRunStatus(db store.Store, ctx context.Context, run *store.Run) map[string]interface{} {
	resourceName := run.ResourceID
	if res, _ := db.GetResource(ctx, run.ResourceID); res != nil {
		resourceName = res.Name
	}
	out := map[string]interface{}{
		"id":              run.ID,
		"name":            run.Name,
		"resource":        resourceName,
		"resource_id":     run.ResourceID,
		"status":          run.Status,
		"kind":            run.Kind,
		"gpu_index":       run.GPUIndex,
		"cwd":             run.Cwd,
		"conda_env":       run.CondaEnv,
		"project_env":     run.ProjectEnv,
		"target_env":      run.TargetEnv,
		"force_reason":    run.ForceReason,
		"preempt_run_id":  run.PreemptRunID,
		"preempt_save":    run.PreemptSave,
		"git_repo_root":   run.GitRepoRoot,
		"git_remote_url":  run.GitRemoteURL,
		"git_branch":      run.GitBranch,
		"git_commit":      run.GitCommit,
		"git_dirty":       run.GitDirty,
		"git_status":      run.GitStatus,
		"git_diff_hash":   run.GitDiffHash,
		"git_diff_path":   run.GitDiffPath,
		"git_allow_dirty": run.GitAllowDirty,
		"resolved_env":    run.ResolvedEnv,
		"resolved_python": run.ResolvedPython,
		"resolved_cwd":    run.ResolvedCwd,
		"tmux":            run.TmuxSession,
		"remote_run_dir":  run.RemoteRunDir,
		"stdout":          strings.TrimRight(run.RemoteRunDir, "/") + "/logs/stdout.log",
		"stderr":          strings.TrimRight(run.RemoteRunDir, "/") + "/logs/stderr.log",
		"metrics":         tryJSONStringSlice(run.MetricPathsJSON),
		"ui_events":       run.UIEventsPath,
	}
	if snapshotPath, err := eventcache.LastSnapshotPath(run.ID); err == nil {
		out["last_snapshot"] = snapshotPath
	}
	if run.ExitCode.Valid {
		out["exit_code"] = run.ExitCode.Int64
	}
	if run.FailureKind != "" {
		out["failure_kind"] = run.FailureKind
	}
	if run.FailureReason != "" {
		out["failure_reason"] = run.FailureReason
	}
	if run.StartedAt.Valid {
		out["started_at"] = run.StartedAt.Time.Format(time.RFC3339)
	}
	if run.FinishedAt.Valid {
		out["finished_at"] = run.FinishedAt.Time.Format(time.RFC3339)
	}
	return out
}

func printShortRunStatus(db store.Store, ctx context.Context, run *store.Run) {
	status := shortRunStatus(db, ctx, run)
	fmt.Printf("%s  %s  resource=%s", status["id"], status["status"], status["resource"])
	if run.GPUIndex >= 0 {
		fmt.Printf("  gpu=%d", run.GPUIndex)
	} else if run.GPUIndex == store.GPUIndexNone {
		fmt.Printf("  gpu=none")
	}
	if run.ExitCode.Valid {
		fmt.Printf("  exit=%d", run.ExitCode.Int64)
	}
	fmt.Println()
	if run.Name != "" {
		fmt.Printf("name:   %s\n", run.Name)
	}
	if run.Cwd != "" {
		fmt.Printf("cwd:    %s\n", run.Cwd)
	}
	if run.CondaEnv != "" {
		fmt.Printf("env:    %s\n", run.CondaEnv)
	}
	if run.TargetEnv != "" {
		fmt.Printf("target_env:      %s\n", run.TargetEnv)
	}
	if run.ForceReason != "" {
		fmt.Printf("force_reason:    %s\n", run.ForceReason)
	}
	if run.PreemptRunID != "" {
		fmt.Printf("preempt_run_id:  %s\n", run.PreemptRunID)
		fmt.Printf("preempt_save:    %v\n", run.PreemptSave)
	}
	if git := runGitLabel(run); git != "" {
		fmt.Printf("git:    %s\n", git)
		if run.GitDirty {
			fmt.Printf("git_dirty:       true\n")
		}
		if run.GitDiffPath != "" {
			fmt.Printf("git_diff:        %s\n", run.GitDiffPath)
		}
	}
	if run.ResolvedEnv != "" {
		fmt.Printf("resolved_env:    %s\n", run.ResolvedEnv)
	}
	if run.FailureKind != "" {
		fmt.Printf("failure_kind:    %s\n", run.FailureKind)
	}
	if run.FailureReason != "" {
		fmt.Printf("failure_reason:  %s\n", run.FailureReason)
	}
	if run.ResolvedPython != "" {
		fmt.Printf("resolved_python: %s\n", run.ResolvedPython)
	}
	if run.ResolvedCwd != "" {
		fmt.Printf("resolved_cwd:    %s\n", run.ResolvedCwd)
	}
	fmt.Printf("run_dir:%s\n", status["remote_run_dir"])
	fmt.Printf("stdout: %s\n", status["stdout"])
	fmt.Printf("stderr: %s\n", status["stderr"])
	if metrics := tryJSONStringSlice(run.MetricPathsJSON); len(metrics) > 0 {
		fmt.Printf("metrics:%s\n", strings.Join(metrics, ","))
	}
	if run.UIEventsPath != "" {
		fmt.Printf("ui_events:%s\n", run.UIEventsPath)
	}
}

func displayRunGPU(gpuIndex int) string {
	switch gpuIndex {
	case store.GPUIndexNone:
		return "none"
	case store.GPUIndexAll:
		return "all"
	default:
		return fmt.Sprintf("%d", gpuIndex)
	}
}

func tryJSONStringSlice(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func runLogsCmd() *cobra.Command {
	var lastN int
	var source string
	var follow, noFollow bool

	cmd := &cobra.Command{
		Use:   "logs [run_id]",
		Short: "Show a run log snapshot, or follow with --follow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil || run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}

			res, err := db.GetResource(cmd.Context(), run.ResourceID)
			if err != nil || res == nil {
				return fmt.Errorf("resource not found")
			}

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)

			exec := executor.NewExecutor(sshPool, db)

			if source == "" {
				source = "stdout"
			}

			if !follow || noFollow || run.Status != store.RunStatusRunning {
				// One-shot read
				lines, err := exec.GetLogSnapshot(cmd.Context(), args[0], source, lastN)
				if err != nil {
					return err
				}
				for _, l := range lines {
					fmt.Println(l.Content)
				}
				return nil
			}

			// Tail mode
			fmt.Printf("Tailing %s logs for %s (Ctrl+C to stop)...\n", source, args[0])
			ch, err := exec.TailLogs(cmd.Context(), args[0], source, lastN)
			if err != nil {
				return err
			}
			for line := range ch {
				fmt.Println(line.Content)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&lastN, "last", 100, "Number of last lines to show")
	cmd.Flags().IntVar(&lastN, "tail", 100, "Number of last lines to show")
	cmd.Flags().StringVar(&source, "source", "stdout", "Log source: stdout or stderr")
	cmd.Flags().BoolVar(&follow, "follow", false, "Follow running logs until interrupted")
	cmd.Flags().BoolVar(&noFollow, "no-follow", false, "Deprecated: snapshot mode is the default")
	return cmd
}

func runEventsCmd() *cobra.Command {
	var tailN int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "events [run_id]",
		Short: "Show structured UI event tail for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := getRunEventSnapshot(cmd.Context(), args[0], tailN, true)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(snapshot)
			}
			for _, line := range snapshot.Lines {
				fmt.Println(line.Content)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tailN, "tail", 50, "Number of latest event lines to read")
	cmd.Flags().IntVar(&tailN, "last", 50, "Alias for --tail")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output parsed events as JSON")
	return cmd
}

func runMetricsCmd() *cobra.Command {
	var tailN int
	var asJSON, latestOnly bool

	cmd := &cobra.Command{
		Use:   "metrics [run_id]",
		Short: "Show latest structured metrics for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := getRunEventSnapshot(cmd.Context(), args[0], tailN, false)
			if err != nil {
				return err
			}
			derived := deriveRunEventSummary(snapshot.Events)
			out := map[string]interface{}{
				"run_id":      snapshot.RunID,
				"path":        snapshot.Path,
				"total_lines": snapshot.TotalLines,
				"metrics":     derived.Metrics,
			}
			if asJSON {
				return printJSON(out)
			}
			if !latestOnly {
				return fmt.Errorf("only --latest metrics are supported for structured events")
			}
			if len(derived.Metrics) == 0 {
				fmt.Println("No metric events found.")
				return nil
			}
			fmt.Printf("%-36s %-12s %-10s %-10s %s\n", "METRIC", "VALUE", "EPOCH", "STEP", "SERIES")
			for _, row := range sortedMetricRows(derived.Metrics) {
				fmt.Printf("%-36s %-12v %-10v %-10v %s\n", truncStr(row.Name, 36), row.Value, blankNil(row.Epoch), blankNil(row.Step), row.Series)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tailN, "tail", 500, "Number of latest event lines to inspect")
	cmd.Flags().IntVar(&tailN, "last", 500, "Alias for --tail")
	cmd.Flags().BoolVar(&latestOnly, "latest", true, "Show latest value per metric")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output latest metrics as JSON")
	return cmd
}

func runEventQualityCmd() *cobra.Command {
	var tailN, maxIssues int
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "event-quality [run_id]",
		Aliases: []string{"events-quality", "quality"},
		Short:   "Scan structured UI events for semantic quality problems",
		Long: `Scan a run's structured UI events for problems that make charts misleading:
long metric names, constants emitted as metrics, missing trial context, epoch
gaps, oversized series labels, and shifted loss axes across trials/variants.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := getRunEventSnapshot(cmd.Context(), args[0], tailN, false)
			if err != nil {
				return err
			}
			report := analyzeEventQuality(snapshot)
			limitEventQualityReport(&report, maxIssues)
			if asJSON {
				return printJSON(report)
			}
			fmt.Printf("Event quality for %s", report.RunID)
			if report.Source != "" {
				fmt.Printf(" (%s)", report.Source)
			}
			fmt.Println()
			fmt.Printf("events: %d  lines: %d  issues: %d\n", report.TotalEvents, report.TotalLines, report.IssueCount)
			if len(report.Issues) == 0 {
				fmt.Println("No obvious event semantic problems found.")
				return nil
			}
			for _, issue := range report.Issues {
				line := ""
				if issue.Line > 0 {
					line = fmt.Sprintf(" line=%d", issue.Line)
				}
				name := ""
				if issue.Name != "" {
					name = fmt.Sprintf(" name=%q", truncStr(issue.Name, 72))
				}
				series := ""
				if issue.Series != "" {
					series = fmt.Sprintf(" series=%q", truncStr(issue.Series, 72))
				}
				fmt.Printf("[%s] %s%s%s%s: %s\n", issue.Severity, issue.Kind, line, name, series, issue.Message)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tailN, "tail", 100000, "Number of latest event lines to inspect")
	cmd.Flags().IntVar(&tailN, "last", 100000, "Alias for --tail")
	cmd.Flags().IntVar(&maxIssues, "max-issues", 200, "Maximum issue details to include; summary keeps total counts")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output quality report as JSON")
	return cmd
}

type EventQualityReport struct {
	RunID       string              `json:"run_id"`
	Path        string              `json:"path,omitempty"`
	Source      string              `json:"source,omitempty"`
	CachePath   string              `json:"cache_path,omitempty"`
	TotalLines  int                 `json:"total_lines"`
	TotalEvents int                 `json:"total_events"`
	IssueCount  int                 `json:"issue_count"`
	ShownIssues int                 `json:"shown_issues"`
	Truncated   bool                `json:"truncated,omitempty"`
	Summary     map[string]int      `json:"summary"`
	Issues      []EventQualityIssue `json:"issues"`
	Advice      []string            `json:"advice,omitempty"`
}

type EventQualityIssue struct {
	Severity string                 `json:"severity"`
	Kind     string                 `json:"kind"`
	Message  string                 `json:"message"`
	Line     int                    `json:"line,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Series   string                 `json:"series,omitempty"`
	Detail   map[string]interface{} `json:"detail,omitempty"`
}

type metricEventSample struct {
	Name   string
	Series string
	Value  float64
	Axis   float64
	Line   int
	Trial  string
}

func analyzeEventQuality(snapshot RunEventSnapshot) EventQualityReport {
	report := EventQualityReport{
		RunID:       snapshot.RunID,
		Path:        snapshot.Path,
		Source:      snapshot.Source,
		CachePath:   snapshot.CachePath,
		TotalLines:  snapshot.TotalLines,
		TotalEvents: len(snapshot.Events),
		Summary:     map[string]int{},
	}
	issues := make([]EventQualityIssue, 0)
	addIssue := func(issue EventQualityIssue) {
		if issue.Severity == "" {
			issue.Severity = "warning"
		}
		issues = append(issues, issue)
		report.Summary[issue.Kind]++
	}

	metricGroups := map[string][]metricEventSample{}
	metricNames := map[string]map[string]bool{}
	for _, ev := range snapshot.Events {
		line := eventLine(ev)
		rawName := eventName(ev)
		if rawName == "" {
			continue
		}
		for _, warning := range structuredEventWarnings(ev) {
			warning.Line = line
			addIssue(warning)
		}
		normalized := copyEventMap(ev)
		normalizeStructuredEvent(normalized)
		typ := strings.ToLower(asEventString(normalized["type"]))
		name := eventName(normalized)
		series := eventSeries(normalized)
		if series != "" && len(series) > 96 {
			addIssue(EventQualityIssue{
				Severity: "warning",
				Kind:     "series_too_long",
				Message:  "series label is very long; put only trial/variant identity here and keep paths/config details in params",
				Line:     line,
				Name:     name,
				Series:   series,
				Detail: map[string]interface{}{
					"series_len": len(series),
				},
			})
		}
		if isMetricEvent(typ, normalized) {
			value, ok := eventFloat(normalized["value"])
			if !ok {
				continue
			}
			if metricNames[name] == nil {
				metricNames[name] = map[string]bool{}
			}
			metricNames[name][series] = true
			axis, axisOK := eventAxis(normalized)
			if axisOK {
				key := name + "\x00" + series
				metricGroups[key] = append(metricGroups[key], metricEventSample{
					Name:   name,
					Series: series,
					Value:  value,
					Axis:   axis,
					Line:   line,
					Trial:  asEventString(normalized["trial"]),
				})
			}
		}
	}

	for _, samples := range metricGroups {
		if len(samples) < 3 {
			continue
		}
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].Axis == samples[j].Axis {
				return samples[i].Line < samples[j].Line
			}
			return samples[i].Axis < samples[j].Axis
		})
		if looksLikeParamName(samples[0].Name) && repeatedConstantMetric(samples) {
			addIssue(EventQualityIssue{
				Severity: "warning",
				Kind:     "constant_as_metric",
				Message:  "metric value is repeated and the name looks like a config parameter; emit it with param()",
				Line:     samples[0].Line,
				Name:     samples[0].Name,
				Series:   samples[0].Series,
				Detail: map[string]interface{}{
					"points": len(samples),
					"value":  samples[0].Value,
				},
			})
		}
		if gap, ok := largestAxisGap(samples); ok {
			addIssue(EventQualityIssue{
				Severity: "warning",
				Kind:     "epoch_gap",
				Message:  "metric axis has a large gap; check that epoch/step is local to this trial and not a global counter",
				Line:     samples[0].Line,
				Name:     samples[0].Name,
				Series:   samples[0].Series,
				Detail: map[string]interface{}{
					"gap":   gap,
					"first": samples[0].Axis,
					"last":  samples[len(samples)-1].Axis,
				},
			})
		}
	}

	for name, seriesSet := range metricNames {
		if len(seriesSet) > 1 && likelySweepMetric(name) {
			for series := range seriesSet {
				if !strings.Contains(series, "trial:") && !strings.Contains(strings.ToLower(series), "trial") {
					addIssue(EventQualityIssue{
						Severity: "warning",
						Kind:     "missing_trial",
						Message:  "same sweep metric appears in multiple series without an explicit trial field; use trial=<id> so curves do not merge or shift",
						Name:     name,
						Series:   series,
					})
				}
			}
		}
	}

	lossByName := map[string]map[string][]metricEventSample{}
	for _, samples := range metricGroups {
		if len(samples) == 0 || !isLossMetric(samples[0].Name) {
			continue
		}
		if lossByName[samples[0].Name] == nil {
			lossByName[samples[0].Name] = map[string][]metricEventSample{}
		}
		lossByName[samples[0].Name][samples[0].Series] = samples
	}
	for name, bySeries := range lossByName {
		if len(bySeries) < 2 {
			continue
		}
		minFirst := 0.0
		firstSet := false
		for _, samples := range bySeries {
			if len(samples) == 0 {
				continue
			}
			if !firstSet || samples[0].Axis < minFirst {
				minFirst = samples[0].Axis
				firstSet = true
			}
		}
		for series, samples := range bySeries {
			if len(samples) == 0 || !firstSet {
				continue
			}
			if samples[0].Axis > minFirst+5 {
				addIssue(EventQualityIssue{
					Severity: "warning",
					Kind:     "loss_axis_offset",
					Message:  "loss curve starts much later than sibling series; check for global epoch/trial counters causing chart offset",
					Line:     samples[0].Line,
					Name:     name,
					Series:   series,
					Detail: map[string]interface{}{
						"series_first_axis": samples[0].Axis,
						"earliest_axis":     minFirst,
					},
				})
			}
		}
	}

	report.Issues = dedupeEventQualityIssues(issues)
	report.IssueCount = len(report.Issues)
	report.ShownIssues = len(report.Issues)
	if report.IssueCount > 0 {
		report.Advice = []string{
			"Use short metric names such as train/loss or val/observed_mse.",
			"Put sweep identity in trial/variant/series fields; keep epoch local to each trial.",
			"Emit config and constants with param(), not metric().",
		}
	}
	return report
}

func limitEventQualityReport(report *EventQualityReport, maxIssues int) {
	if maxIssues <= 0 || len(report.Issues) <= maxIssues {
		report.ShownIssues = len(report.Issues)
		return
	}
	report.Issues = report.Issues[:maxIssues]
	report.ShownIssues = len(report.Issues)
	report.Truncated = true
}

func dedupeEventQualityIssues(issues []EventQualityIssue) []EventQualityIssue {
	seen := map[string]bool{}
	out := make([]EventQualityIssue, 0, len(issues))
	for _, issue := range issues {
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", issue.Kind, issue.Name, issue.Series, issue.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	return out
}

func copyEventMap(ev map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(ev))
	for k, v := range ev {
		out[k] = v
	}
	return out
}

func eventLine(ev map[string]interface{}) int {
	switch raw := ev["_line"].(type) {
	case int:
		return raw
	case float64:
		return int(raw)
	default:
		return 0
	}
}

func eventAxis(ev map[string]interface{}) (float64, bool) {
	if epoch, ok := eventFloat(ev["epoch"]); ok {
		return epoch, true
	}
	if step, ok := eventFloat(ev["step"]); ok {
		return step, true
	}
	return 0, false
}

func isLongMetricIdentity(name, series string) bool {
	if len(name) > 64 || len(series) > 96 {
		return true
	}
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		if len(parts) > 2 && len(strings.Join(parts[:len(parts)-1], "/")) > 20 {
			return true
		}
	}
	if strings.Contains(name, "/home/") || strings.Contains(name, "/Users/") || strings.Count(name, "_") >= 6 {
		return true
	}
	return false
}

func looksLikeParamName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	paramNames := map[string]bool{
		"batch_size": true, "epochs": true, "seed": true, "gpu": true, "num_workers": true,
		"model": true, "input_mode": true, "target_dim": true, "target_prefix": true,
		"seq_len": true, "pred_len": true, "patience": true, "max_trials": true,
		"trial_count": true, "selection_metric": true, "python": true,
	}
	if paramNames[lower] {
		return true
	}
	for _, suffix := range []string{"_dir", "_path", "_csv", "_root", "_file"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func repeatedConstantMetric(samples []metricEventSample) bool {
	if len(samples) < 3 {
		return false
	}
	first := samples[0].Value
	for _, sample := range samples[1:] {
		if sample.Value != first {
			return false
		}
	}
	return true
}

func largestAxisGap(samples []metricEventSample) (float64, bool) {
	if len(samples) < 4 {
		return 0, false
	}
	gaps := make([]float64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		gap := samples[i].Axis - samples[i-1].Axis
		if gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) < 3 {
		return 0, false
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	maxGap := sorted[len(sorted)-1]
	if median <= 0 {
		median = 1
	}
	return maxGap, maxGap >= 5 && maxGap >= median*4
}

func likelySweepMetric(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "loss") || strings.Contains(lower, "mse") || strings.Contains(lower, "mae") || strings.Contains(lower, "metric")
}

func isLossMetric(name string) bool {
	return strings.Contains(strings.ToLower(name), "loss")
}

func runSnapshotCmd() *cobra.Command {
	var tailN int
	var asJSON, refresh bool

	cmd := &cobra.Command{
		Use:   "snapshot [run_id]",
		Short: "Show low-noise run status plus latest events/metrics",
		Long: `Show a low-noise run snapshot for agents.

By default this does not refresh remote tmux status. It reads the cached run
record plus the structured UI event tail, so agents can monitor training from
events instead of repeatedly probing status/logs. Pass --refresh when you need
an explicit remote status check.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil || run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}
			if refresh && store.IsRunRefreshableStatus(run.Status) {
				sshPool := executor.NewSSHPool(10 * time.Second)
				loadSSHKeys(sshPool)
				exec := executor.NewExecutor(sshPool, db)
				if refreshed, err := exec.CheckRunStatus(cmd.Context(), run.ID); err == nil && refreshed != nil {
					run = refreshed
				}
			}
			eventSnapshot, eventErr := getRunEventSnapshot(cmd.Context(), run.ID, tailN, false)
			summary := RunEventSummary{}
			if eventErr == nil {
				summary = deriveRunEventSummary(eventSnapshot.Events)
			}
			out := map[string]interface{}{
				"run": shortRunStatus(db, cmd.Context(), run),
				"events": map[string]interface{}{
					"path":         run.UIEventsPath,
					"source":       eventSnapshot.Source,
					"cache_path":   eventSnapshot.CachePath,
					"total_lines":  eventSnapshot.TotalLines,
					"tail_count":   len(eventSnapshot.Events),
					"error":        errorString(eventErr),
					"remote_error": eventSnapshot.RemoteError,
					"cache_error":  eventSnapshot.CacheError,
				},
				"monitoring": map[string]interface{}{
					"preferred_tool":                  "aexp_get_run_snapshot",
					"fallback_tool":                   "aexp_tail_run_logs",
					"suggested_poll_interval_sec":     60,
					"max_backoff_interval_sec":        120,
					"refresh_status_only_when_needed": true,
				},
				"progress": summary.Progress,
				"metrics":  summary.Metrics,
				"params":   summary.Params,
				"notes":    summary.Notes,
			}
			if asJSON {
				return printJSON(out)
			}
			fmt.Printf("%s  %s  kind=%s  gpu=%s\n", run.ID, run.Status, run.Kind, displayRunGPU(run.GPUIndex))
			if run.Name != "" {
				fmt.Printf("name: %s\n", run.Name)
			}
			if run.UIEventsPath != "" {
				fmt.Printf("events: %s", run.UIEventsPath)
				if eventErr != nil {
					fmt.Printf(" (%s)", eventErr)
				}
				fmt.Println()
			}
			if len(summary.Progress) > 0 {
				fmt.Println("progress:")
				for _, p := range sortedProgressRows(summary.Progress) {
					fmt.Printf("  %s: %v/%v", p.Name, p.Current, blankNil(p.Total))
					if p.Percent != nil {
						fmt.Printf(" (%.1f%%)", *p.Percent)
					}
					fmt.Println()
				}
			}
			if len(summary.Metrics) > 0 {
				fmt.Println("latest metrics:")
				for _, m := range sortedMetricRows(summary.Metrics) {
					fmt.Printf("  %s = %v", m.Name, m.Value)
					if m.Epoch != nil {
						fmt.Printf(" epoch=%v", *m.Epoch)
					}
					if m.Step != nil {
						fmt.Printf(" step=%v", *m.Step)
					}
					if m.Series != "" {
						fmt.Printf(" series=%s", m.Series)
					}
					fmt.Println()
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tailN, "tail", 500, "Number of latest event lines to inspect")
	cmd.Flags().IntVar(&tailN, "last", 500, "Alias for --tail")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Refresh remote status before returning snapshot")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output snapshot as JSON")
	return cmd
}

type RunEventSnapshot struct {
	RunID       string                   `json:"run_id"`
	Path        string                   `json:"path"`
	Source      string                   `json:"source,omitempty"`
	CachePath   string                   `json:"cache_path,omitempty"`
	CacheError  string                   `json:"cache_error,omitempty"`
	RemoteError string                   `json:"remote_error,omitempty"`
	TotalLines  int                      `json:"total_lines"`
	Lines       []executor.LogLine       `json:"lines,omitempty"`
	Events      []map[string]interface{} `json:"events"`
}

type RunEventSummary struct {
	Progress map[string]ProgressRow   `json:"progress"`
	Metrics  map[string]MetricRow     `json:"metrics"`
	Params   map[string]interface{}   `json:"params"`
	Notes    []map[string]interface{} `json:"notes"`
}

type ProgressRow struct {
	Name    string   `json:"name"`
	Current float64  `json:"current"`
	Total   *float64 `json:"total,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
	Time    *float64 `json:"time,omitempty"`
}

type MetricRow struct {
	Name   string   `json:"name"`
	Series string   `json:"series,omitempty"`
	Value  float64  `json:"value"`
	Epoch  *float64 `json:"epoch,omitempty"`
	Step   *float64 `json:"step,omitempty"`
	Time   *float64 `json:"time,omitempty"`
}

func getRunEventSnapshot(ctx context.Context, runID string, tailN int, includeLines bool) (RunEventSnapshot, error) {
	db := openDB()
	defer db.Close()
	run, err := db.GetRun(ctx, runID)
	if err != nil || run == nil {
		if err != nil {
			return RunEventSnapshot{}, err
		}
		return RunEventSnapshot{}, fmt.Errorf("run %s not found", runID)
	}
	if run.UIEventsPath == "" {
		return RunEventSnapshot{RunID: run.ID}, fmt.Errorf("run %s has no ui events path", run.ID)
	}
	if store.IsRunTerminalStatus(run.Status) {
		if cacheLines, cachePath, cacheErr := eventcache.Read(run.ID, tailN); cacheErr == nil && len(cacheLines) > 0 {
			lines := executorLinesFromEventCache(run.ID, run.UIEventsPath, cacheLines)
			snapshot := eventSnapshotFromLines(run.ID, run.UIEventsPath, "cache", lines, includeLines)
			snapshot.CachePath = cachePath
			return snapshot, nil
		}
	}
	sshPool := executor.NewSSHPool(10 * time.Second)
	loadSSHKeys(sshPool)
	exec := executor.NewExecutor(sshPool, db)
	lines, err := exec.GetLogFileSnapshot(ctx, run.ID, run.UIEventsPath, tailN)
	if err != nil {
		cacheLines, cachePath, cacheErr := eventcache.Read(run.ID, tailN)
		if cacheErr == nil && len(cacheLines) > 0 {
			lines = executorLinesFromEventCache(run.ID, run.UIEventsPath, cacheLines)
			snapshot := eventSnapshotFromLines(run.ID, run.UIEventsPath, "cache", lines, includeLines)
			snapshot.CachePath = cachePath
			snapshot.RemoteError = err.Error()
			return snapshot, nil
		}
		snapshot := RunEventSnapshot{
			RunID:       run.ID,
			Path:        run.UIEventsPath,
			Source:      "remote",
			RemoteError: err.Error(),
		}
		if cacheErr != nil {
			snapshot.CacheError = cacheErr.Error()
		}
		return snapshot, err
	}
	snapshot := eventSnapshotFromLines(run.ID, run.UIEventsPath, "remote", lines, includeLines)
	cachePath, cacheErr := eventcache.WriteSnapshot(run.ID, eventCacheLinesFromExecutor(lines))
	snapshot.CachePath = cachePath
	if cacheErr != nil {
		snapshot.CacheError = cacheErr.Error()
	}
	return snapshot, nil
}

func eventSnapshotFromLines(runID, path, source string, lines []executor.LogLine, includeLines bool) RunEventSnapshot {
	events := parseRunEventLines(lines)
	total := 0
	if len(lines) > 0 {
		total = lines[len(lines)-1].LineNo
	}
	snapshot := RunEventSnapshot{
		RunID:      runID,
		Path:       path,
		Source:     source,
		TotalLines: total,
		Events:     events,
	}
	if includeLines {
		snapshot.Lines = lines
	}
	return snapshot
}

func eventCacheLinesFromExecutor(lines []executor.LogLine) []eventcache.Line {
	out := make([]eventcache.Line, 0, len(lines))
	for _, line := range lines {
		out = append(out, eventcache.Line{LineNo: line.LineNo, Content: line.Content})
	}
	return out
}

func executorLinesFromEventCache(runID, source string, lines []eventcache.Line) []executor.LogLine {
	out := make([]executor.LogLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, executor.LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  line.LineNo,
			Content: line.Content,
		})
	}
	return out
}

func parseRunEventLines(lines []executor.LogLine) []map[string]interface{} {
	events := make([]map[string]interface{}, 0, len(lines))
	for _, line := range lines {
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(line.Content), &ev); err != nil {
			continue
		}
		ev["_line"] = line.LineNo
		events = append(events, ev)
	}
	return events
}

func deriveRunEventSummary(events []map[string]interface{}) RunEventSummary {
	out := RunEventSummary{
		Progress: map[string]ProgressRow{},
		Metrics:  map[string]MetricRow{},
		Params:   map[string]interface{}{},
	}
	for _, ev := range events {
		typ := strings.ToLower(asEventString(ev["type"]))
		switch {
		case isProgressEvent(typ, ev):
			row, ok := progressRowFromEvent(ev)
			if ok {
				out.Progress[row.Name] = row
			}
		case isMetricEvent(typ, ev):
			row, ok := metricRowFromEvent(ev)
			if ok {
				out.Metrics[metricKey(row)] = row
			}
		case typ == "param" || typ == "params" || typ == "hparam" || typ == "config":
			if name := eventName(ev); name != "" {
				out.Params[name] = ev["value"]
			}
		case typ == "note" || typ == "log" || typ == "message" || typ == "warning":
			out.Notes = append(out.Notes, ev)
			if len(out.Notes) > 5 {
				out.Notes = out.Notes[len(out.Notes)-5:]
			}
		}
	}
	return out
}

func isProgressEvent(typ string, ev map[string]interface{}) bool {
	if typ == "progress" {
		return true
	}
	_, hasCurrent := eventFloat(ev["current"])
	_, hasTotal := eventFloat(ev["total"])
	return hasCurrent && hasTotal
}

func isMetricEvent(typ string, ev map[string]interface{}) bool {
	if typ == "metric" || typ == "metrics" || typ == "eval" || typ == "scalar" {
		return true
	}
	_, hasValue := eventFloat(ev["value"])
	return hasValue && eventName(ev) != ""
}

func progressRowFromEvent(ev map[string]interface{}) (ProgressRow, bool) {
	name := eventName(ev)
	current, ok := eventFloat(ev["current"])
	if name == "" || !ok {
		return ProgressRow{}, false
	}
	row := ProgressRow{Name: name, Current: current}
	if total, ok := eventFloat(ev["total"]); ok {
		row.Total = &total
		if total != 0 {
			pct := current / total * 100
			row.Percent = &pct
		}
	}
	if t, ok := eventFloat(ev["time"]); ok {
		row.Time = &t
	}
	return row, true
}

func metricRowFromEvent(ev map[string]interface{}) (MetricRow, bool) {
	name := eventName(ev)
	value, ok := eventFloat(ev["value"])
	if name == "" || !ok {
		return MetricRow{}, false
	}
	row := MetricRow{Name: name, Value: value, Series: eventSeries(ev)}
	if epoch, ok := eventFloat(ev["epoch"]); ok {
		row.Epoch = &epoch
	}
	if step, ok := eventFloat(ev["step"]); ok {
		row.Step = &step
	}
	if t, ok := eventFloat(ev["time"]); ok {
		row.Time = &t
	}
	return row, true
}

func eventName(ev map[string]interface{}) string {
	for _, key := range []string{"name", "metric", "key", "label"} {
		if s := asEventString(ev[key]); s != "" {
			return s
		}
	}
	return ""
}

func eventSeries(ev map[string]interface{}) string {
	parts := make([]string, 0, 8)
	for _, key := range []string{"series", "run", "variant", "trial", "seed", "fold", "split", "stage"} {
		if s := asEventString(ev[key]); s != "" {
			part := s
			switch key {
			case "trial", "seed", "fold":
				part = key + ":" + s
			}
			if !stringSliceContains(parts, part) {
				parts = append(parts, part)
			}
		}
	}
	return strings.Join(parts, "/")
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func metricKey(row MetricRow) string {
	if row.Series == "" {
		return row.Name
	}
	return row.Series + "/" + row.Name
}

func eventFloat(v interface{}) (float64, bool) {
	switch raw := v.(type) {
	case float64:
		return raw, true
	case float32:
		return float64(raw), true
	case int:
		return float64(raw), true
	case int64:
		return float64(raw), true
	case json.Number:
		f, err := raw.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func asEventString(v interface{}) string {
	switch raw := v.(type) {
	case string:
		return strings.TrimSpace(raw)
	case fmt.Stringer:
		return strings.TrimSpace(raw.String())
	default:
		return ""
	}
}

func sortedMetricRows(rows map[string]MetricRow) []MetricRow {
	out := make([]MetricRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return metricKey(out[i]) < metricKey(out[j]) })
	return out
}

func sortedProgressRows(rows map[string]ProgressRow) []ProgressRow {
	out := make([]ProgressRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func blankNil(v *float64) interface{} {
	if v == nil {
		return ""
	}
	return *v
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func runCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel [run_id]",
		Short: "Cancel a running experiment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)

			exec := executor.NewExecutor(sshPool, db)

			if err := exec.Cancel(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Cancelled run %s\n", args[0])
			return nil
		},
	}
}

func runArchiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "archive [run_id]",
		Aliases: []string{"trash"},
		Short:   "Move a finished run to trash",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}
			if runIsActive(run) {
				return fmt.Errorf("run %s is %s; active runs cannot be moved to trash", run.ID, run.Status)
			}
			if err := db.ArchiveRun(cmd.Context(), run.ID); err != nil {
				return err
			}
			fmt.Printf("Moved run %s to trash\n", run.ID)
			return nil
		},
	}
}

func runRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore [run_id]",
		Short: "Restore a run from trash",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}
			if err := db.RestoreRun(cmd.Context(), run.ID); err != nil {
				return err
			}
			fmt.Printf("Restored run %s\n", run.ID)
			return nil
		},
	}
}

func runDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete [run_id]",
		Aliases: []string{"rm"},
		Short:   "Logically delete a run from trash",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}
			if runIsActive(run) {
				return fmt.Errorf("run %s is %s; active runs cannot be deleted", run.ID, run.Status)
			}
			if !run.ArchivedAt.Valid && !run.DeletedAt.Valid {
				return fmt.Errorf("run %s is not in trash; run `aexp run archive %s` first", run.ID, run.ID)
			}
			if err := db.DeleteRunLogically(cmd.Context(), run.ID); err != nil {
				return err
			}
			fmt.Printf("Logically deleted run %s\n", run.ID)
			return nil
		},
	}
}

func runIsActive(run *store.Run) bool {
	return store.IsRunActiveLifecycleStatus(run.Status)
}

func runMarkCmd() *cobra.Command {
	var actor, kind, title, statement, bodyMD, bodyMDFile, reason, evidence string
	var attachments []string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "mark <run_id>",
		Short: "Legacy compatibility: attach a historical note to one run",
		Long: `Legacy compatibility command for historical RunMark records.
Do not use this for new research reasoning. Append a Project journal entry and
optionally link one or more Runs instead.

Historical note shape:
  --title is the note title.
  --statement is the short one-sentence claim shown in lists.
  --body-md or --body-md-file is the Markdown body shown when the note is opened.
  --attach copies a local image/file into ~/.aexp and appends Markdown image links
    such as ![caption](aexp-attachment://att_xxx) when the body has no attachment URI.

Attachment syntax:
  --attach /path/to/plot.png
  --attach /path/to/plot.png|Prediction window plot

Examples:
  aexp run mark run_ABC --title "IR baseline confirms signal" --statement "mAP improves over target-only" --body-md "Validation selected checkpoint improves mAP." --evidence "logs/train.log"
  aexp run mark run_ABC --kind key_result --title "Output-window plots generated" --statement "Plots explain the 192/96 delta" --body-md-file notes.md --attach outputs/plot.png|Top error cases
  aexp run mark run_ABC --kind failure --title "Conda env mismatch" --statement "Python resolved to the system interpreter" --evidence "logs/setup.log"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			run, err := db.GetRun(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run == nil {
				return fmt.Errorf("run %s not found", args[0])
			}
			if bodyMDFile != "" {
				data, err := os.ReadFile(expandPath(bodyMDFile))
				if err != nil {
					return fmt.Errorf("read body md file: %w", err)
				}
				bodyMD = string(data)
			}

			markID := genID("mark_")
			markAttachments, err := copyRunMarkAttachments(markID, attachments)
			if err != nil {
				return err
			}
			bodyMD = appendAttachmentRefs(strings.TrimSpace(bodyMD), markAttachments)
			if strings.TrimSpace(statement) == "" {
				statement = deriveRunMarkStatement(bodyMD, reason, evidence)
			}
			if strings.TrimSpace(bodyMD) == "" {
				bodyMD = legacyRunMarkBody(reason, evidence)
			}
			if strings.TrimSpace(title) == "" && strings.TrimSpace(statement) == "" && strings.TrimSpace(bodyMD) == "" && strings.TrimSpace(reason) == "" && strings.TrimSpace(evidence) == "" {
				return fmt.Errorf("title, statement, body-md, reason, or evidence is required")
			}

			mark := store.RunMark{
				ID:          markID,
				RunID:       run.ID,
				Actor:       strings.TrimSpace(actor),
				Kind:        strings.TrimSpace(kind),
				Title:       strings.TrimSpace(title),
				Statement:   strings.TrimSpace(statement),
				BodyMD:      strings.TrimSpace(bodyMD),
				Reason:      strings.TrimSpace(reason),
				Evidence:    strings.TrimSpace(evidence),
				Attachments: markAttachments,
			}
			if mark.Actor == "" {
				mark.Actor = "agent"
			}
			if mark.Kind == "" {
				mark.Kind = "key_result"
			}

			if err := db.SaveRunMark(cmd.Context(), &mark); err != nil {
				return fmt.Errorf("save run mark: %w", err)
			}
			if err := db.SaveRunMarkAttachments(cmd.Context(), mark.ID, markAttachments); err != nil {
				return fmt.Errorf("save run mark attachments: %w", err)
			}
			if asJSON {
				return printJSON(mark)
			}
			fmt.Printf("Marked run %s as %s (%s)\n", run.ID, mark.Kind, mark.ID)
			for _, attachment := range markAttachments {
				fmt.Printf("Attachment %s: ![%s](aexp-attachment://%s)\n", attachment.ID, attachment.Caption, attachment.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "agent", "Actor writing the mark")
	cmd.Flags().StringVar(&kind, "kind", "key_result", "Mark kind, e.g. key_result, failure, note, followup")
	cmd.Flags().StringVar(&title, "title", "", "Short title for the finding")
	cmd.Flags().StringVar(&statement, "statement", "", "One-sentence statement shown in mark lists")
	cmd.Flags().StringVar(&bodyMD, "body-md", "", "Markdown body shown when opening the note")
	cmd.Flags().StringVar(&bodyMDFile, "body-md-file", "", "Read Markdown body from a file")
	cmd.Flags().StringVar(&reason, "reason", "", "Why this run matters")
	cmd.Flags().StringVar(&evidence, "evidence", "", "Lightweight Markdown/plain-text evidence, log paths, or artifact paths")
	cmd.Flags().StringSliceVar(&attachments, "attach", nil, "Copy a local file/image into this mark; syntax PATH or PATH|caption, repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

func runMarksCmd() *cobra.Command {
	var runID, actor, kind string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "marks [run_id]",
		Short: "Legacy compatibility: list historical RunMark records",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if runID != "" && runID != args[0] {
					return fmt.Errorf("run id specified twice: positional %s conflicts with --run %s", args[0], runID)
				}
				runID = args[0]
			}
			db := openDB()
			defer db.Close()

			filter := store.RunMarkFilter{
				RunID: runID,
				Actor: actor,
				Kind:  kind,
				Limit: limit,
			}
			marks, err := db.ListRunMarks(cmd.Context(), filter)
			if err != nil {
				return fmt.Errorf("list run marks: %w", err)
			}
			if asJSON {
				return printJSON(marks)
			}
			if len(marks) == 0 {
				fmt.Println("No run marks found.")
				return nil
			}
			fmt.Printf("%-14s %-15s %-12s %-12s %-28s %s\n", "TIME", "RUN_ID", "KIND", "ACTOR", "TITLE", "REASON")
			for _, m := range marks {
				fmt.Printf("%-14s %-15s %-12s %-12s %-28s %s\n",
					m.CreatedAt.Format("01-02 15:04"),
					truncStr(m.RunID, 15),
					truncStr(m.Kind, 12),
					truncStr(m.Actor, 12),
					truncStr(m.Title, 28),
					truncStr(m.Reason, 60),
				)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&runID, "run", "", "Filter by run id")
	cmd.Flags().StringVar(&actor, "actor", "", "Filter by actor")
	cmd.Flags().StringVar(&kind, "kind", "", "Filter by mark kind")
	cmd.Flags().IntVar(&limit, "limit", 20, "Max marks to show")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

// --- exec ---

func execCmd() *cobra.Command {
	var resourceName, cwd, projectEnv, condaEnv, apiURL string
	var timeout int
	var asJSON, shellMode, dryRun, force, direct, refreshEnv bool

	cmd := &cobra.Command{
		Use:   "exec [flags] -- <command>",
		Short: "Run a one-shot command on a resource (no Run created)",
		Long: `Execute a command on a registered resource for inspection/ops.
Does NOT create a Run — use 'run submit' for experiment tasks.

By default, exec first tries the local aexp API (127.0.0.1:8080) so a running
aexp serve process can reuse its warm SSH pool. If no local API is reachable,
exec falls back to direct SSH execution in this CLI process. Use --direct to
skip the local API path.

If the command matches a known long-running pattern (training, etc.),
a warning is printed and execution is refused unless --force is set.
Use --dry-run to preview what would happen without executing.

Command parsing:
  - One argument after -- is treated as a remote shell command string.
  - Multiple arguments are shell-quoted as argv-like tokens.
  - Use --shell to join multiple arguments as a raw shell string.

The --cwd flag is an aexp management constraint under resource root_dir. It is
not a strong remote shell sandbox; an explicit cd inside the command is still
interpreted by the remote shell.

Subcommands:
  aexp exec history   Show recent exec event history
  aexp exec show      Show details of a specific exec event

Examples:
  aexp exec --resource mu -- 'nvidia-smi'
  aexp exec --resource mu --cwd /workspace/project --project-env auto -- 'python -V'
  aexp exec --resource mu -- bash -lc 'cd /workspace && python script.py'
  aexp exec --resource mu --cwd /workspace -- 'ls outputs/'
  aexp exec --resource mu --shell -- echo start '&&' nvidia-smi
  aexp exec --resource mu --json -- 'du -sh /workspace/*'
  aexp exec --resource mu --dry-run -- python train.py
  aexp exec --resource mu --force -- python train.py
  aexp exec history --resource mu
  aexp exec show exec_ABC123`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := buildExecCommand(args, shellMode)
			if projectEnv != "" && projectEnv != "raw" && projectEnv != "auto" {
				return fmt.Errorf("--project-env must be raw or auto")
			}

			// Long-running detection
			if isLong, reason := executor.DetectLongRunningCmd(command); isLong {
				if dryRun {
					fmt.Printf("[dry-run] Command: %s\n", command)
					fmt.Printf("[dry-run] Long-running detected: %s\n", reason)
					fmt.Println("[dry-run] Would refuse execution. Use 'aexp run submit --kind setup --no-gpu' for setup tasks, or '--kind formal --gpu-index <n>' for experiment runs.")
					return nil
				}
				if !force {
					return fmt.Errorf("command looks like a long-running job (%s); use 'aexp run submit --kind setup --no-gpu' for setup tasks, or '--kind formal --gpu-index <n>' for experiment runs; use --force only for bounded inspection", reason)
				}
				fmt.Fprintf(os.Stderr, "warning: long-running command detected (%s), proceeding due to --force\n", reason)
			}

			if dryRun {
				fmt.Printf("[dry-run] Command: %s\n", command)
				fmt.Printf("[dry-run] Resource: %s\n", resourceName)
				if cwd != "" {
					fmt.Printf("[dry-run] Cwd: %s\n", cwd)
				}
				if projectEnv != "" {
					fmt.Printf("[dry-run] Project env: %s\n", projectEnv)
				}
				if refreshEnv {
					fmt.Println("[dry-run] Refresh project env: true")
				}
				if condaEnv != "" {
					fmt.Printf("[dry-run] Conda env override: %s\n", condaEnv)
				}
				fmt.Printf("[dry-run] Timeout: %ds\n", timeout)
				return nil
			}

			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}

			req := executor.ExecRequest{
				ResourceID:        res.ID,
				Command:           command,
				Cwd:               cwd,
				ProjectEnv:        projectEnv,
				CondaEnv:          condaEnv,
				TimeoutSec:        timeout,
				Actor:             "cli",
				RefreshProjectEnv: refreshEnv,
			}

			var result *executor.ExecResult
			if !direct {
				apiResult, usedAPI, apiErr := execViaLocalAPI(cmd.Context(), apiURL, req)
				if usedAPI {
					if apiErr != nil {
						return apiErr
					}
					result = apiResult
				}
			}
			if result == nil {
				sshPool := executor.NewSSHPool(10 * time.Second)
				loadSSHKeys(sshPool)
				exec := executor.NewExecutor(sshPool, db)
				directResult, err := exec.Exec(cmd.Context(), req)
				if err != nil {
					return err
				}
				result = directResult
			}

			return printExecResult(result, asJSON)
		},
	}

	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory on remote")
	cmd.Flags().StringVar(&projectEnv, "project-env", "", "Runtime env strategy for exec: raw or auto (.venv, then conda_env, then raw shell)")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment override for --project-env auto")
	cmd.Flags().BoolVar(&refreshEnv, "refresh-env", false, "Ignore cached project profile and re-detect the environment")
	cmd.Flags().IntVar(&timeout, "timeout", 30, "Timeout in seconds (max 300)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON (stdout/stderr/exit_code)")
	cmd.Flags().BoolVar(&shellMode, "shell", false, "Join command arguments as a raw remote shell string")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview command and long-running check without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Execute even if long-running pattern detected")
	cmd.Flags().BoolVar(&direct, "direct", false, "Skip local aexp API fast path and execute from this CLI process")
	cmd.Flags().StringVar(&apiURL, "api", defaultLocalAPIURL(), "Local aexp API base URL for exec fast path")
	_ = cmd.MarkFlagRequired("resource")

	cmd.AddCommand(execHistoryCmd())
	cmd.AddCommand(execShowCmd())

	return cmd
}

func execViaLocalAPI(ctx context.Context, apiURL string, req executor.ExecRequest) (*executor.ExecResult, bool, error) {
	apiURL = strings.TrimRight(apiURL, "/")
	if apiURL == "" {
		return nil, false, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, true, err
	}

	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	apiCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec+10)*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(apiCtx, http.MethodPost, apiURL+"/exec", bytes.NewReader(body))
	if err != nil {
		return nil, true, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		if isLocalAPIUnavailable(err) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("local aexp API exec failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error   string `json:"error"`
			Details string `json:"details"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Details != "" {
			return nil, true, fmt.Errorf("local aexp API exec failed: %s", apiErr.Details)
		}
		if apiErr.Error != "" {
			return nil, true, fmt.Errorf("local aexp API exec failed: %s", apiErr.Error)
		}
		return nil, true, fmt.Errorf("local aexp API exec failed: HTTP %d", resp.StatusCode)
	}

	var result executor.ExecResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, true, fmt.Errorf("decode local aexp API exec result: %w", err)
	}
	return &result, true, nil
}

func printExecResult(result *executor.ExecResult, asJSON bool) error {
	if asJSON {
		return printJSON(result)
	}
	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("exit code %d", result.ExitCode)
	}
	return nil
}

func defaultLocalAPIURL() string {
	if v := strings.TrimRight(os.Getenv("AEXP_API_URL"), "/"); v != "" {
		return v
	}
	return "http://127.0.0.1:8080/api/v1"
}

func isLocalAPIUnavailable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connect: connection reset") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "Client.Timeout exceeded") ||
		strings.Contains(msg, "context deadline exceeded")
}

func execHistoryCmd() *cobra.Command {
	var resourceName, actor string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent exec event history",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			filter := store.ExecEventFilter{
				Actor: actor,
				Limit: limit,
			}
			if resourceName != "" {
				res, err := db.GetResourceByName(cmd.Context(), resourceName)
				if err != nil || res == nil {
					return fmt.Errorf("resource %s not found", resourceName)
				}
				filter.ResourceID = res.ID
			}

			events, err := db.ListExecEvents(cmd.Context(), filter)
			if err != nil {
				return fmt.Errorf("list exec events: %w", err)
			}

			if asJSON {
				return printJSON(events)
			}

			if len(events) == 0 {
				fmt.Println("No exec events found.")
				return nil
			}

			fmt.Printf("%-14s %-12s %-8s %-30s %s\n", "TIME", "RESOURCE", "ACTOR", "COMMAND", "EXIT")
			for _, ev := range events {
				res, _ := db.GetResource(cmd.Context(), ev.ResourceID)
				resName := ev.ResourceID
				if res != nil {
					resName = res.Name
				}
				cmdTrunc := ev.Command
				if len(cmdTrunc) > 30 {
					cmdTrunc = cmdTrunc[:27] + "..."
				}
				exitStr := "-"
				if ev.ExitCode.Valid {
					exitStr = fmt.Sprintf("%d", ev.ExitCode.Int64)
				}
				fmt.Printf("%-14s %-12s %-8s %-30s %s\n",
					ev.CreatedAt.Format("01-02 15:04"),
					resName,
					ev.Actor,
					cmdTrunc,
					exitStr,
				)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&resourceName, "resource", "", "Filter by resource name")
	cmd.Flags().StringVar(&actor, "actor", "", "Filter by actor (cli, api, agent)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Max events to show")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

func execShowCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show <event_id>",
		Short: "Show details of a specific exec event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			ev, err := db.GetExecEvent(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("get exec event: %w", err)
			}
			if ev == nil {
				return fmt.Errorf("exec event %s not found", args[0])
			}

			if asJSON {
				return printJSON(ev)
			}

			res, _ := db.GetResource(cmd.Context(), ev.ResourceID)
			resName := ev.ResourceID
			if res != nil {
				resName = res.Name
			}

			fmt.Printf("Event:      %s\n", ev.ID)
			fmt.Printf("Resource:   %s\n", resName)
			fmt.Printf("Actor:      %s\n", ev.Actor)
			fmt.Printf("Command:    %s\n", ev.Command)
			if ev.Cwd != "" {
				fmt.Printf("Cwd:        %s\n", ev.Cwd)
			}
			fmt.Printf("Started:    %s\n", ev.StartedAt.Format(time.RFC3339))
			if ev.FinishedAt.Valid {
				fmt.Printf("Finished:   %s\n", ev.FinishedAt.Time.Format(time.RFC3339))
			}
			fmt.Printf("Duration:   %d ms\n", ev.DurationMs)
			if ev.ExitCode.Valid {
				fmt.Printf("Exit Code:  %d\n", ev.ExitCode.Int64)
			}
			if ev.StdoutTail != "" {
				fmt.Printf("\n--- stdout (tail) ---\n%s\n", ev.StdoutTail)
			}
			if ev.StderrTail != "" {
				fmt.Printf("\n--- stderr (tail) ---\n%s\n", ev.StderrTail)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

// --- helpers ---

func copyRunMarkAttachments(markID string, specs []string) ([]store.RunMarkAttachment, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	baseDir := expandPath(filepath.Join("~/.aexp", "attachments", "run_marks", markID))
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create mark attachment dir: %w", err)
	}

	attachments := make([]store.RunMarkAttachment, 0, len(specs))
	for _, spec := range specs {
		source, caption := parseAttachmentSpec(spec)
		if source == "" {
			continue
		}
		source = expandPath(source)
		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("stat attachment %s: %w", source, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("attachment %s is a directory; attach files only", source)
		}
		id := genID("att_")
		filename := filepath.Base(source)
		if caption == "" {
			caption = strings.TrimSuffix(filename, filepath.Ext(filename))
		}
		dest := filepath.Join(baseDir, id+"_"+filename)
		if err := copyFile(source, dest); err != nil {
			return nil, fmt.Errorf("copy attachment %s: %w", source, err)
		}
		attachments = append(attachments, store.RunMarkAttachment{
			ID:        id,
			MarkID:    markID,
			Filename:  filename,
			LocalPath: dest,
			Mime:      detectMime(dest, filename),
			Caption:   caption,
			Size:      info.Size(),
			CreatedAt: time.Now(),
		})
	}
	return attachments, nil
}

func parseAttachmentSpec(spec string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(spec), "|", 2)
	source := strings.TrimSpace(parts[0])
	caption := ""
	if len(parts) == 2 {
		caption = strings.TrimSpace(parts[1])
	}
	return source, caption
}

func copyFile(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func detectMime(path string, filename string) string {
	if mt := mime.TypeByExtension(filepath.Ext(filename)); mt != "" {
		return mt
	}
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	var buf [512]byte
	n, _ := file.Read(buf[:])
	if n > 0 {
		return http.DetectContentType(buf[:n])
	}
	return "application/octet-stream"
}

func appendAttachmentRefs(body string, attachments []store.RunMarkAttachment) string {
	if len(attachments) == 0 || strings.Contains(body, "aexp-attachment://") {
		return body
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(body))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("## Attachments\n\n")
	for _, attachment := range attachments {
		caption := attachment.Caption
		if caption == "" {
			caption = attachment.Filename
		}
		b.WriteString("![")
		b.WriteString(strings.ReplaceAll(caption, "]", "\\]"))
		b.WriteString("](aexp-attachment://")
		b.WriteString(attachment.ID)
		b.WriteString(")\n\n")
	}
	return strings.TrimSpace(b.String())
}

func deriveRunMarkStatement(bodyMD, reason, evidence string) string {
	for _, source := range []string{reason, bodyMD, evidence} {
		for _, line := range strings.Split(source, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if line != "" && !strings.HasPrefix(line, "![") && !strings.HasPrefix(line, "```") {
				if len(line) > 180 {
					return line[:177] + "..."
				}
				return line
			}
		}
	}
	return ""
}

func legacyRunMarkBody(reason, evidence string) string {
	parts := []string{}
	if strings.TrimSpace(reason) != "" {
		parts = append(parts, strings.TrimSpace(reason))
	}
	if strings.TrimSpace(evidence) != "" && strings.TrimSpace(evidence) != strings.TrimSpace(reason) {
		parts = append(parts, strings.TrimSpace(evidence))
	}
	return strings.Join(parts, "\n\n")
}

func openDB() *store.SQLite {
	dbPath := expandPath("~/.aexp/aexp.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		logger.Error("create database directory", "path", filepath.Dir(dbPath), "error", err)
		os.Exit(1)
	}
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	return db
}

func expandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// loadSSHKeys tries to register all commonly available SSH keys into the pool.
// Returns the first key path found (for passing to functions that need it).
func loadSSHKeys(pool *executor.SSHPool) string {
	candidates := []string{
		"~/.aexp/id_ed25519",
		"~/.ssh/id_ed25519",
		"~/.ssh/id_rsa",
	}
	first := ""
	for _, p := range candidates {
		full := expandPath(p)
		if _, err := os.Stat(full); err == nil {
			pool.AddKey(full)
			if first == "" {
				first = full
			}
		}
	}
	return first
}

func buildLocalSSHResource(name string, rootDir string) (*store.Resource, error) {
	if rootDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get current directory: %w", err)
		}
		rootDir = wd
	}
	if name == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "localhost"
		}
		name = "local-" + sanitizeLocalName(host)
	}
	userName := os.Getenv("USER")
	if userName == "" {
		userName = os.Getenv("LOGNAME")
	}
	if userName == "" {
		return nil, fmt.Errorf("cannot determine local user")
	}
	keyPath := firstDefaultSSHKey()
	if keyPath == "" {
		return nil, fmt.Errorf("no default SSH key found; create ~/.ssh/id_ed25519 or register localhost manually with --auth-ref")
	}
	condaBase, condaInit := detectLocalConda()
	osType := localOSType()
	return &store.Resource{
		ID:         genID("rsrc_"),
		Name:       name,
		Type:       store.ResourceTypeSSH,
		Host:       "127.0.0.1",
		OSType:     osType,
		Port:       22,
		User:       userName,
		AuthRef:    keyPath,
		RootDir:    rootDir,
		RemotePath: executor.EffectiveRemotePath(&store.Resource{OSType: osType}),
		CondaBase:  condaBase,
		CondaInit:  condaInit,
		Tags:       "local",
		Status:     store.ResourceStatusUnknown,
	}, nil
}

func testLocalSSH(ctx context.Context, r *store.Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		"-o", "StrictHostKeyChecking=no",
		"-i", r.AuthRef,
		"-p", fmt.Sprintf("%d", r.Port),
		r.User + "@" + r.Host,
		"echo ok",
	}
	out, err := osexec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh localhost failed: %w (%s). On macOS, enable Remote Login and ensure the public key is in ~/.ssh/authorized_keys", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstDefaultSSHKey() string {
	for _, p := range []string{"~/.aexp/id_ed25519", "~/.ssh/id_ed25519", "~/.ssh/id_rsa"} {
		full := expandPath(p)
		if _, err := os.Stat(full); err == nil {
			return full
		}
	}
	return ""
}

func detectLocalConda() (string, string) {
	conda, err := osexec.LookPath("conda")
	if err != nil {
		return "", ""
	}
	out, err := osexec.Command(conda, "info", "--base").Output()
	if err != nil {
		return "", ""
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return "", ""
	}
	return base, filepath.Join(base, "etc/profile.d/conda.sh")
}

func localOSType() string {
	return normalizeOSType(runtime.GOOS)
}

func normalizeOSType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "darwin", "mac", "macos", "osx":
		return "macos"
	case "linux", "ubuntu", "debian", "centos", "fedora", "arch":
		return "linux"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func sanitizeLocalName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "machine"
	}
	return out
}

func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func joinArgs(args []string) string {
	result := ""
	for i, a := range args {
		if i > 0 {
			result += " "
		}
		result += a
	}
	return result
}

func buildExecCommand(args []string, shellMode bool) string {
	if shellMode || len(args) == 1 {
		return joinArgs(args)
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, cliShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func cliShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func genID(prefix string) string {
	id, _ := gonanoid.Generate("0123456789abcdefghijklmnopqrstuvwxyz", 12)
	return prefix + id
}
