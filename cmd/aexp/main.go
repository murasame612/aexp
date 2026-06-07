package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/spf13/cobra"

	"github.com/ziwu/aexp/internal/api"
	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/explore"
	"github.com/ziwu/aexp/internal/monitor"
	"github.com/ziwu/aexp/internal/store"
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
It does not need to be installed on the remote host.`,
		Version:      version,
		SilenceUsage: true,
	}

	root.AddCommand(
		serveCmd(),
		initCmd(),
		resourceCmd(),
		runCmd(),
		execCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
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
				return startServeDaemon(logPath)
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
			mon := monitor.NewManager(db, monitorPool, 3*time.Second, logger)

			if err := mon.Start(); err != nil {
				return fmt.Errorf("start monitor: %w", err)
			}
			defer mon.Stop()

			// Generate API token
			apiToken, _ := gonanoid.Generate("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 32)
			fmt.Fprintf(os.Stderr, "\n=== API Token: %s ===\n", apiToken)
			fmt.Fprintf(os.Stderr, "Use this token in Authorization header: Bearer %s\n\n", apiToken)
			if !requireTokenLocal {
				fmt.Fprintln(os.Stderr, "Loopback requests from localhost/127.0.0.1/::1 do not require the token.")
				fmt.Fprintln(os.Stderr, "Use --require-token-local to require it for local requests too.")
				fmt.Fprintln(os.Stderr)
			}

			srv := api.NewServer(db, exec, mon, logger, apiToken, !requireTokenLocal)
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

func startServeDaemon(logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()

	childArgs := stripFlag(os.Args[1:], "--daemon")
	cmd := osexec.Command(os.Args[0], childArgs...)
	cmd.Env = append(os.Environ(), "AEXP_DAEMON_CHILD=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	fmt.Fprintf(os.Stderr, "aexp server started in background (pid %d)\n", cmd.Process.Pid)
	fmt.Fprintf(os.Stderr, "logs: %s\n", logPath)
	return nil
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

// --- init ---

func initCmd() *cobra.Command {
	return &cobra.Command{
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
			return nil
		},
	}
}

// --- resource ---

func resourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resource",
		Short: "Manage compute resources",
	}

	cmd.AddCommand(resourceListCmd())
	cmd.AddCommand(resourceAddCmd())
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
				fmt.Printf("%-16s %-20s %-24s %-24s %-12s %-8s %-8s %-8s %s\n", "NAME", "HOST", "ROOT_DIR", "CONDA_BASE", "CONDA", "GPU", "STATUS", "CPU", "RAM")
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
					fmt.Printf("%-16s %-20s %-24s %-24s %-12s %-8s %-8s %-8s %s\n",
						truncStr(r.Name, 16), truncStr(r.Host, 20), truncStr(r.RootDir, 24), truncStr(r.CondaBase, 24), truncStr(r.CondaEnv, 12), truncStr(r.GPUIndices, 8), r.Status, cpuStr, ramStr)
				}
				return nil
			}

			fmt.Printf("%-16s %-20s %-8s %-8s %-8s  %s\n", "NAME", "HOST", "STATUS", "CPU", "RAM", "GPU")
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
				fmt.Printf("%-16s %-20s %-8s %-8s %-8s  %s\n",
					truncStr(r.Name, 16), r.Host, r.Status, cpuStr, ramStr, gpuStr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show root_dir, conda env, and configured GPU indices")
	return cmd
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
	var name, host, user, rootDir, condaBase, condaInit, condaEnv, gpuIndices, tags, authRef, socksProxy, proxyCommand string
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
				Port:         port,
				User:         user,
				AuthRef:      authRef,
				RootDir:      rootDir,
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
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().StringVar(&user, "user", "root", "SSH user")
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "Workspace root directory (required)")
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

func resourceUpdateCmd() *cobra.Command {
	var name, host, user, rootDir, condaBase, condaInit, condaEnv, gpuIndices, tags, authRef, socksProxy, proxyCommand string
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
			setString("user", &r.User, user)
			setString("auth-ref", &r.AuthRef, authRef)
			setString("root-dir", &r.RootDir, rootDir)
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
	cmd.Flags().IntVar(&port, "port", 0, "SSH port")
	cmd.Flags().StringVar(&user, "user", "", "SSH user")
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "Workspace root directory")
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
	}

	cmd.AddCommand(runSubmitCmd())
	cmd.AddCommand(runListCmd())
	cmd.AddCommand(runStatusCmd())
	cmd.AddCommand(runLogsCmd())
	cmd.AddCommand(runCancelCmd())

	return cmd
}

func runSubmitCmd() *cobra.Command {
	var resource, name, cwd, condaEnv, kind string
	var gpuIndex int
	var shellMode, force bool
	var logPaths, artifactPaths, metricPaths []string

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
    aexp run submit --resource mu --shell -- 'echo start; python train.py | tee log'`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var submitReq executor.SubmitRequest

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
			submitReq.Cwd = cwd
			submitReq.CondaEnv = condaEnv
			submitReq.LogPaths = logPaths
			submitReq.ArtifactPaths = artifactPaths
			submitReq.MetricPaths = metricPaths

			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resource)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resource)
			}
			submitReq.ResourceID = res.ID

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)

			exec := executor.NewExecutor(sshPool, db)

			run, err := exec.Submit(cmd.Context(), submitReq)
			if err != nil {
				return err
			}

			fmt.Printf("Submitted run %s on %s\n", run.ID, resource)
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Run name")
	cmd.Flags().StringVar(&kind, "kind", "formal", "Run kind: smoke, pilot, formal, ablation")
	cmd.Flags().IntVar(&gpuIndex, "gpu-index", -1, "GPU index to use (-1 for all)")
	cmd.Flags().BoolVar(&shellMode, "shell", false, "Shell mode: interpret command via bash -lc")
	cmd.Flags().BoolVar(&force, "force", false, "Skip GPU slot lock, allow concurrent runs on same resource/GPU")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment")
	cmd.Flags().StringSliceVar(&logPaths, "log-paths", nil, "Log file globs")
	cmd.Flags().StringSliceVar(&artifactPaths, "artifact-paths", nil, "Artifact file globs")
	cmd.Flags().StringSliceVar(&metricPaths, "metric-paths", nil, "Metric file globs")

	return cmd
}

func runListCmd() *cobra.Command {
	var status, resource string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			filter := store.RunFilter{Status: status, Limit: 50}
			if resource != "" {
				res, _ := db.GetResourceByName(cmd.Context(), resource)
				if res != nil {
					filter.ResourceID = res.ID
				}
			}

			runs, err := db.ListRuns(cmd.Context(), filter)
			if err != nil {
				return err
			}

			if asJSON {
				return printJSON(runs)
			}

			fmt.Printf("%-15s %-25s %-20s %-12s %s\n", "RUN_ID", "NAME", "RESOURCE", "STATUS", "COMMAND")
			for _, r := range runs {
				name := r.Name
				if name == "" {
					name = "-"
				}
				fmt.Printf("%-15s %-25s %-20s %-12s %s\n",
					r.ID, truncStr(name, 25), r.ResourceID, r.Status, truncStr(r.Command, 60))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource name")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

func runStatusCmd() *cobra.Command {
	return &cobra.Command{
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
			if run.Status == store.RunStatusRunning || run.Status == store.RunStatusStarting {
				sshPool := executor.NewSSHPool(10 * time.Second)
				loadSSHKeys(sshPool)
				exec := executor.NewExecutor(sshPool, db)
				refreshed, err := exec.CheckRunStatus(cmd.Context(), run.ID)
				if err == nil && refreshed != nil {
					run = refreshed
				}
			}

			fmt.Printf("ID:        %s\n", run.ID)
			fmt.Printf("Name:      %s\n", run.Name)
			fmt.Printf("Resource:  %s\n", run.ResourceID)
			fmt.Printf("Status:    %s\n", run.Status)
			fmt.Printf("Kind:      %s\n", run.Kind)
			fmt.Printf("Command:   %s\n", run.Command)
			fmt.Printf("CWD:       %s\n", run.Cwd)
			fmt.Printf("tmux:      %s\n", run.TmuxSession)
			if run.GPUIndex >= 0 {
				fmt.Printf("GPU:       %d\n", run.GPUIndex)
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
}

func runLogsCmd() *cobra.Command {
	var lastN int

	cmd := &cobra.Command{
		Use:   "logs [run_id]",
		Short: "Tail run logs",
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

			if run.Status != store.RunStatusRunning {
				// One-shot read
				lines, err := exec.GetLogSnapshot(cmd.Context(), args[0], "stdout", lastN)
				if err != nil {
					return err
				}
				for _, l := range lines {
					fmt.Println(l.Content)
				}
				return nil
			}

			// Tail mode
			fmt.Printf("Tailing logs for %s (Ctrl+C to stop)...\n", args[0])
			ch, err := exec.TailLogs(cmd.Context(), args[0], "stdout", lastN)
			if err != nil {
				return err
			}
			for line := range ch {
				fmt.Println(line.Content)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&lastN, "last", 200, "Number of last lines to show")
	return cmd
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

// --- exec ---

func execCmd() *cobra.Command {
	var resourceName, cwd string
	var timeout int
	var asJSON, shellMode bool

	cmd := &cobra.Command{
		Use:   "exec [flags] -- <command>",
		Short: "Run a one-shot command on a resource (no Run created)",
		Long: `Execute a command on a registered resource for inspection/ops.
Does NOT create a Run — use 'run submit' for experiment tasks.

Command parsing:
  - One argument after -- is treated as a remote shell command string.
  - Multiple arguments are shell-quoted as argv-like tokens.
  - Use --shell to join multiple arguments as a raw shell string.

The --cwd flag is an aexp management constraint under resource root_dir. It is
not a strong remote shell sandbox; an explicit cd inside the command is still
interpreted by the remote shell.

Examples:
  aexp exec --resource mu -- 'nvidia-smi'
  aexp exec --resource mu -- bash -lc 'cd /workspace && python script.py'
  aexp exec --resource mu --cwd /workspace -- 'ls outputs/'
  aexp exec --resource mu --shell -- echo start '&&' nvidia-smi
  aexp exec --resource mu --json -- 'du -sh /workspace/*'`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := buildExecCommand(args, shellMode)

			db := openDB()
			defer db.Close()

			res, err := db.GetResourceByName(cmd.Context(), resourceName)
			if err != nil || res == nil {
				return fmt.Errorf("resource %s not found", resourceName)
			}

			sshPool := executor.NewSSHPool(10 * time.Second)
			loadSSHKeys(sshPool)

			exec := executor.NewExecutor(sshPool, db)

			result, err := exec.Exec(cmd.Context(), executor.ExecRequest{
				ResourceID: res.ID,
				Command:    command,
				Cwd:        cwd,
				TimeoutSec: timeout,
				Actor:      "cli",
			})
			if err != nil {
				return err
			}

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
		},
	}

	cmd.Flags().StringVar(&resourceName, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory on remote")
	cmd.Flags().IntVar(&timeout, "timeout", 30, "Timeout in seconds (max 300)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON (stdout/stderr/exit_code)")
	cmd.Flags().BoolVar(&shellMode, "shell", false, "Join command arguments as a raw remote shell string")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

// --- helpers ---

func openDB() *store.SQLite {
	dbPath := expandPath("~/.aexp/aexp.db")
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
