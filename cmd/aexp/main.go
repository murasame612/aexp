package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
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
It does not need to be installed on the remote host.

Agent workflow:
  - Use "aexp run submit" for experiments.
  - Use "aexp run logs/status" to inspect results.
  - After interpreting a run, attach a lightweight finding with
    "aexp run mark <run_id> --title ... --reason ... --evidence ...".
    These marks are shown in the web UI so important results survive context loss.`,
		Version:      version,
		SilenceUsage: true,
	}

	root.AddCommand(
		agentCmd(),
		doctorCmd(),
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

// --- agent ---

func agentCmd() *cobra.Command {
	var asJSON bool

	steps := []string{
		"Check resources: aexp resource list --verbose",
		"Inspect remote: aexp exec --resource <name> --cwd <path> -- 'pwd; nvidia-smi'",
		"Submit formal run: aexp run submit --resource <name> --kind formal --name <run-name> --cwd <project> --conda-env <env> --gpu-index 0 --metric-paths <metrics.json> --log-paths '<logs/**/*>' -- python train.py ...",
		"Inspect run: aexp run status <run_id>; aexp run logs <run_id>",
		"Preserve finding: aexp run mark <run_id> --title ... --reason ... --evidence ...",
	}
	rules := []string{
		"Use run submit for experiments; use exec only for inspection/ops.",
		"Use --kind formal for paper evidence; never treat smoke tests as real results.",
		"Always provide --metric-paths and --log-paths for formal runs.",
		"--cwd must be under the resource root_dir; update root_dir if the project lives elsewhere.",
		"After interpreting logs/metrics/artifacts, write a run mark so results survive context loss.",
	}

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

// --- doctor ---

type doctorCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
	Detail   string `json:"detail,omitempty"`
}

type doctorReport struct {
	Resource                 string        `json:"resource"`
	Cwd                      string        `json:"cwd"`
	CondaEnv                 string        `json:"conda_env"`
	Checks                   []doctorCheck `json:"checks"`
	RecommendedSubmitCommand string        `json:"recommended_submit_command"`
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

			if asJSON {
				return printJSON(report)
			}
			printDoctorReport(report)
			for _, check := range report.Checks {
				if !check.OK {
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
		return pool.Exec(ctx, res.Host, res.Port, res.User, res.AuthRef, command, res.SocksProxy, res.ProxyCommand)
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

	if condaEnv != "" {
		condaCmd := remoteCondaPrefix(res, condaEnv) + "python -c 'import sys; print(sys.executable)'"
		out, stderr, err = execRemote(condaCmd)
		checkOK = err == nil && strings.TrimSpace(out) != ""
		if checkOK {
			remoteOK++
		}
		add("conda env exists", checkOK, strings.TrimSpace(firstNonEmpty(out, stderr, errString(err))))
	} else {
		add("conda env exists", true, "not requested")
	}

	pythonCmd := "cd " + cliShellQuote(cwd) + " && " + remoteCondaPrefix(res, condaEnv) + "python -c 'import sys; print(sys.version.split()[0])'"
	out, stderr, err = execRemote(pythonCmd)
	checkOK = err == nil && strings.TrimSpace(out) != ""
	if checkOK {
		remoteOK++
	}
	add("python available", checkOK, strings.TrimSpace(firstNonEmpty(out, stderr, errString(err))))

	cudaCmd := remoteCondaPrefix(res, condaEnv) + "python - <<'PY'\ntry:\n import torch\n print('torch cuda', torch.cuda.is_available(), torch.cuda.device_count())\nexcept Exception as e:\n print('torch unavailable', e)\nPY"
	out, stderr, err = execRemote(cudaCmd)
	if err == nil && strings.Contains(out, "torch cuda True") {
		remoteOK++
		add("cuda visible", true, strings.TrimSpace(out))
	} else {
		nvout, nvstderr, nverr := execRemote("command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L")
		checkOK = nverr == nil && strings.TrimSpace(nvout) != ""
		if checkOK {
			remoteOK++
		}
		add("cuda visible", checkOK, strings.TrimSpace(firstNonEmpty(out, nvout, stderr, nvstderr, errString(err), errString(nverr))))
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
	cleanRoot := filepath.Clean(rootDir)
	cleanCwd := filepath.Clean(resolved)
	return cleanCwd != cleanRoot && !strings.HasPrefix(cleanCwd, cleanRoot+"/")
}

func recommendedSubmitCommand(resourceName, cwd, condaEnv string, gpuIndex int) string {
	parts := []string{
		"aexp run submit",
		"--resource " + cliShellQuote(resourceName),
		"--kind formal",
		"--name <run-name>",
		"--cwd " + cliShellQuote(cwd),
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

func printDoctorReport(report doctorReport) {
	fmt.Println("AEXP Doctor")
	fmt.Println()
	fmt.Printf("resource: %s\n", report.Resource)
	fmt.Printf("cwd:      %s\n", report.Cwd)
	if report.CondaEnv != "" {
		fmt.Printf("conda:    %s\n", report.CondaEnv)
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
	fmt.Println("recommended submit command:")
	fmt.Println(report.RecommendedSubmitCommand)
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
				fmt.Printf("%-16s %-20s %-8s %-24s %-24s %-12s %-8s %-8s %-8s %s\n", "NAME", "HOST", "OS", "ROOT_DIR", "CONDA_BASE", "CONDA", "GPU", "STATUS", "CPU", "RAM")
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
					fmt.Printf("%-16s %-20s %-8s %-24s %-24s %-12s %-8s %-8s %-8s %s\n",
						truncStr(r.Name, 16), truncStr(r.Host, 20), truncStr(r.OSType, 8), truncStr(r.RootDir, 24), truncStr(r.CondaBase, 24), truncStr(r.CondaEnv, 12), truncStr(r.GPUIndices, 8), r.Status, cpuStr, ramStr)
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
	var name, host, osType, user, rootDir, condaBase, condaInit, condaEnv, gpuIndices, tags, authRef, socksProxy, proxyCommand string
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
	var name, host, osType, user, rootDir, condaBase, condaInit, condaEnv, gpuIndices, tags, authRef, socksProxy, proxyCommand string
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

For agent-human collaboration, runs are the raw execution records and marks are
the interpretation layer. After inspecting logs, metrics, or artifacts, write a
finding with:

  aexp run mark <run_id> --title ... --reason ... --evidence ...

The web UI shows these findings on the Dashboard and inside each run detail.`,
	}

	cmd.AddCommand(runSubmitCmd())
	cmd.AddCommand(runListCmd())
	cmd.AddCommand(runStatusCmd())
	cmd.AddCommand(runLogsCmd())
	cmd.AddCommand(runCancelCmd())
	cmd.AddCommand(runMarkCmd())
	cmd.AddCommand(runMarksCmd())

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
			fmt.Printf("After inspection, record important findings with:\n  aexp run mark %s --title \"...\" --reason \"...\" --evidence \"logs/...\"\n", run.ID)
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
	var asJSON bool

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
			if run.Status == store.RunStatusRunning || run.Status == store.RunStatusStarting {
				sshPool := executor.NewSSHPool(10 * time.Second)
				loadSSHKeys(sshPool)
				exec := executor.NewExecutor(sshPool, db)
				refreshed, err := exec.CheckRunStatus(cmd.Context(), run.ID)
				if err == nil && refreshed != nil {
					run = refreshed
				}
			}

			if asJSON {
				return printJSON(run)
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

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
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

func runMarkCmd() *cobra.Command {
	var actor, kind, title, reason, evidence string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "mark <run_id>",
		Short: "Attach an agent/human finding to a run",
		Long: `Attach a lightweight interpretation to a run without changing the run record.
Use this after reading logs or artifacts so important findings survive context loss.

Examples:
  aexp run mark run_ABC --title "IR baseline confirms signal" --reason "mAP improves over target-only" --evidence "logs/train.log"
  aexp run mark run_ABC --kind failure --title "Conda env mismatch" --evidence "python resolved to system interpreter"`,
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
			if strings.TrimSpace(title) == "" && strings.TrimSpace(reason) == "" && strings.TrimSpace(evidence) == "" {
				return fmt.Errorf("title, reason, or evidence is required")
			}

			mark := store.RunMark{
				ID:       genID("mark_"),
				RunID:    run.ID,
				Actor:    strings.TrimSpace(actor),
				Kind:     strings.TrimSpace(kind),
				Title:    strings.TrimSpace(title),
				Reason:   strings.TrimSpace(reason),
				Evidence: strings.TrimSpace(evidence),
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
			if asJSON {
				return printJSON(mark)
			}
			fmt.Printf("Marked run %s as %s (%s)\n", run.ID, mark.Kind, mark.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "agent", "Actor writing the mark")
	cmd.Flags().StringVar(&kind, "kind", "key_result", "Mark kind, e.g. key_result, failure, note, followup")
	cmd.Flags().StringVar(&title, "title", "", "Short title for the finding")
	cmd.Flags().StringVar(&reason, "reason", "", "Why this run matters")
	cmd.Flags().StringVar(&evidence, "evidence", "", "Lightweight Markdown/plain-text evidence, log paths, or artifact paths")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	return cmd
}

func runMarksCmd() *cobra.Command {
	var runID, actor, kind string
	var limit int
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "marks",
		Short: "List run findings",
		RunE: func(cmd *cobra.Command, args []string) error {
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
	var resourceName, cwd string
	var timeout int
	var asJSON, shellMode, dryRun, force bool

	cmd := &cobra.Command{
		Use:   "exec [flags] -- <command>",
		Short: "Run a one-shot command on a resource (no Run created)",
		Long: `Execute a command on a registered resource for inspection/ops.
Does NOT create a Run — use 'run submit' for experiment tasks.

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

			// Long-running detection
			if isLong, reason := executor.DetectLongRunningCmd(command); isLong {
				if dryRun {
					fmt.Printf("[dry-run] Command: %s\n", command)
					fmt.Printf("[dry-run] Long-running detected: %s\n", reason)
					fmt.Println("[dry-run] Would refuse execution. Use 'aexp run submit' for training jobs.")
					return nil
				}
				if !force {
					return fmt.Errorf("command looks like a long-running job (%s); use 'aexp run submit' for training jobs, or --force to override", reason)
				}
				fmt.Fprintf(os.Stderr, "warning: long-running command detected (%s), proceeding due to --force\n", reason)
			}

			if dryRun {
				fmt.Printf("[dry-run] Command: %s\n", command)
				fmt.Printf("[dry-run] Resource: %s\n", resourceName)
				if cwd != "" {
					fmt.Printf("[dry-run] Cwd: %s\n", cwd)
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview command and long-running check without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Execute even if long-running pattern detected")
	_ = cmd.MarkFlagRequired("resource")

	cmd.AddCommand(execHistoryCmd())
	cmd.AddCommand(execShowCmd())

	return cmd
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
	return &store.Resource{
		ID:        genID("rsrc_"),
		Name:      name,
		Type:      store.ResourceTypeSSH,
		Host:      "127.0.0.1",
		OSType:    localOSType(),
		Port:      22,
		User:      userName,
		AuthRef:   keyPath,
		RootDir:   rootDir,
		CondaBase: condaBase,
		CondaInit: condaInit,
		Tags:      "local",
		Status:    store.ResourceStatusUnknown,
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
