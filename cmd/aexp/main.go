package main

import (
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/spf13/cobra"

	"github.com/ziwu/aexp/internal/api"
	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/explore"
	"github.com/ziwu/aexp/internal/mcp"
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
		eventCmd(),
		mcpCmd(),
		projectCmd(),
		syncCmd(),
		serveCmd(),
		initCmd(),
		resourceCmd(),
		runCmd(),
		execCmd(),
	)

	if filepath.Base(os.Args[0]) == "aexp-event" {
		root.SetArgs(append([]string{"event"}, os.Args[1:]...))
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
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
	short := "Install aexp MCP config into Codex/Claude Code"
	if uninstall {
		use = "uninstall"
		short = "Remove aexp MCP config from Codex/Claude Code"
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
	cmd.Flags().StringVar(&opts.Target, "target", opts.Target, "MCP client target: codex, claude, or all")
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
		}
	}
	return plan, nil
}

func parseMCPTargets(target string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "all":
		return []string{"codex", "claude"}, nil
	case "codex":
		return []string{"codex"}, nil
	case "claude", "claude-code", "cc":
		return []string{"claude"}, nil
	default:
		return nil, fmt.Errorf("--target must be codex, claude, or all")
	}
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

func startServeDaemon(logPath string, host string, port int) error {
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

// --- agent ---

func agentCmd() *cobra.Command {
	var asJSON bool

	steps := []string{
		"Check resources: aexp resource list --verbose",
		"Inspect remote: aexp exec --resource <name> --cwd <path> --project-env auto -- 'pwd; python -V; nvidia-smi'",
		"Submit formal run: aexp run submit --resource <name> --kind formal --name <run-name> --cwd <project> --conda-env <env> --gpu-index 0 --metric-paths <metrics.json> --log-paths '<logs/**/*>' -- python train.py ...",
		"Submit setup task: aexp run submit --resource <name> --kind setup --no-gpu --cwd <project> --shell -- 'python -m pip install -r requirements.txt'",
		"Inspect run: aexp run status <run_id> --short; aexp run logs <run_id> --tail 100 --no-follow",
		"Preserve finding: aexp run mark <run_id> --title ... --reason ... --evidence ...",
	}
	rules := []string{
		"aexp runs locally; do not ssh aexp. Use aexp exec/run to dispatch to registered resources.",
		"Use run submit for experiments; use exec only for inspection/ops.",
		"For project checks, prefer exec --project-env auto so .venv or resource conda_env is activated when available.",
		"Use --kind formal for paper evidence; never treat setup/smoke runs as real results.",
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
	Resource                 string                `json:"resource"`
	Cwd                      string                `json:"cwd"`
	CondaEnv                 string                `json:"conda_env"`
	Project                  *store.ProjectProfile `json:"project,omitempty"`
	ProjectConfig            string                `json:"project_config,omitempty"`
	Checks                   []doctorCheck         `json:"checks"`
	RecommendedSubmitCommand string                `json:"recommended_submit_command"`
	Recommended              []string              `json:"recommended,omitempty"`
	RecommendedFixes         []string              `json:"recommended_fixes,omitempty"`
	Recipes                  []doctorRecipe        `json:"recipes,omitempty"`
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
		Short: "Detect and validate project runtime profiles",
		Long: `Project profiles describe how to enter a project on a resource:
resource + cwd + environment strategy + result/log globs.`,
	}
	cmd.AddCommand(projectDetectCmd())
	cmd.AddCommand(projectDoctorCmd())
	cmd.AddCommand(projectInitCmd())
	cmd.AddCommand(projectRunCmd())
	cmd.AddCommand(projectSyncCmd())
	return cmd
}

type projectFileConfig struct {
	Path       string
	Resource   string
	Cwd        string
	Env        string
	CondaEnv   string
	DefaultGPU *int
	Logs       []string
	Metrics    []string
	Artifacts  []string
	Commands   map[string]projectFileCommand
	Sync       projectFileSync
}

type projectFileCommand struct {
	Name      string
	Command   string
	Kind      string
	GPUIndex  *int
	NoGPU     bool
	Logs      []string
	Metrics   []string
	Artifacts []string
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
	fmt.Fprintf(&b, "resource: %s\n", cfg.Resource)
	fmt.Fprintf(&b, "cwd: %s\n", cfg.Cwd)
	fmt.Fprintf(&b, "env: %s\n", cfg.Env)
	if cfg.CondaEnv != "" {
		fmt.Fprintf(&b, "conda_env: %s\n", cfg.CondaEnv)
	}
	fmt.Fprintf(&b, "default_gpu: %d\n\n", cfg.DefaultGPU)
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
	fmt.Fprintf(&b, "# Optional structured UI events inside scripts:\n")
	fmt.Fprintf(&b, "#   aexp event metric train/loss 0.23 --epoch 1\n")
	fmt.Fprintf(&b, "#   aexp event progress train 1 --total 100\n")
	fmt.Fprintf(&b, "#   aexp-event note \"finished validation\"\n")
	fmt.Fprintf(&b, "# Python scripts can also import: from aexp_events import metric, progress, param, note\n")
	appendProjectTrainConfig(&b, cfg)
	return b.String()
}

func writeYAMLList(b *strings.Builder, key string, values []string) {
	fmt.Fprintf(b, "%s:\n", key)
	for _, value := range values {
		fmt.Fprintf(b, "  - %s\n", value)
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
	var configPath, resourceName, cwd, name, kind, projectEnv, condaEnv string
	var gpuIndex int
	var noGPU, force, dryRun, refreshEnv bool
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

			if resourceName == "" {
				resourceName = cfg.Resource
			}
			if resourceName == "" {
				return fmt.Errorf("resource is required: set resource: in %s or pass --resource", cfg.Path)
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
			if projectEnv == "" {
				projectEnv = cfg.Env
			}
			if projectEnv == "" {
				projectEnv = executor.ProjectEnvAuto
			}
			if condaEnv == "" {
				condaEnv = cfg.CondaEnv
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
			submitReq := executor.SubmitRequest{
				ResourceID:        resourceName,
				Name:              name,
				Kind:              kind,
				GPUIndex:          effectiveGPU,
				Force:             force,
				Cwd:               cwd,
				CondaEnv:          condaEnv,
				ProjectEnv:        projectEnv,
				LogPaths:          logPaths,
				MetricPaths:       metricPaths,
				ArtifactPaths:     artifactPaths,
				Program:           "bash",
				Args:              []string{"-lc", entry.Command},
				RefreshProjectEnv: refreshEnv,
			}
			if dryRun {
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
	cmd.Flags().StringVar(&projectEnv, "project-env", "", "Override runtime env strategy: auto or raw")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Override conda environment")
	cmd.Flags().BoolVar(&refreshEnv, "refresh-env", false, "Ignore cached project profile and re-detect the environment")
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
		Path:     resolved,
		Commands: map[string]projectFileCommand{},
	}
	section := ""
	listKey := ""
	lines := strings.Split(string(data), "\n")
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
			if value == "" {
				switch normalizeProjectKey(key) {
				case "logs", "metrics", "artifacts":
					listKey = key
				case "sync":
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
	return cfg, nil
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
		case "defaultgpu", "gpu":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
			cfg.DefaultGPU = &n
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
		case "logs":
			cfg.Logs = append(cfg.Logs, value)
		case "metrics":
			cfg.Metrics = append(cfg.Metrics, value)
		case "artifacts":
			cfg.Artifacts = append(cfg.Artifacts, value)
		}
	case "sync":
		if normalizeProjectKey(key) == "exclude" || normalizeProjectKey(key) == "excludes" {
			cfg.Sync.Excludes = append(cfg.Sync.Excludes, value)
		}
	default:
		entry := ensureProjectCommand(cfg, section)
		switch normalizeProjectKey(key) {
		case "logs":
			entry.Logs = append(entry.Logs, value)
		case "metrics":
			entry.Metrics = append(entry.Metrics, value)
		case "artifacts":
			entry.Artifacts = append(entry.Artifacts, value)
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
	if req.RefreshProjectEnv {
		args = append(args, "--refresh-env")
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
	fmt.Printf("gpu:      %s\n", runGPULabelText(req.GPUIndex))
	printStringList("logs", req.LogPaths)
	printStringList("metrics", req.MetricPaths)
	printStringList("artifacts", req.ArtifactPaths)
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

	launchCtx := ctx
	var cancel context.CancelFunc
	if launchTimeoutSec > 0 {
		launchCtx, cancel = context.WithTimeout(ctx, time.Duration(launchTimeoutSec)*time.Second)
		defer cancel()
	}
	createdID := ""
	run, err := exec.SubmitWithOptions(launchCtx, submitReq, executor.SubmitOptions{
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
	fmt.Printf("After inspection, record important findings with:\n  aexp run mark %s --title \"...\" --reason \"...\" --evidence \"logs/...\"\n", run.ID)
	return nil
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
	if !dryRun {
		if _, err := osexec.LookPath("rsync"); err != nil {
			return fmt.Errorf("local rsync not found: %w", err)
		}
		if err := checkRemoteRsyncViaExec(ctx, exec, res, 20); err != nil {
			return fmt.Errorf("remote rsync missing. Install rsync on %s or use remote-pull from a source with rsync: %w", res.Name, err)
		}
		if err := ensureRemoteDir(ctx, exec, res, target); err != nil {
			return err
		}
	}
	rsyncArgs := buildRsyncArgs(res, dryRun, deleteExtra, resolvedExcludes, nil)
	rsyncArgs = append(rsyncArgs, source, remoteRsyncSpec(res, target))
	if dryRun {
		printSyncDryRunExcludes(profile, resolvedExcludes, excludeSources)
		fmt.Println(joinShellArgs(append([]string{"rsync"}, rsyncArgs...)))
		return nil
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
				if resourceName == "" {
					resourceName = cfg.Resource
				}
				if cwd == "" {
					cwd = cfg.Cwd
				}
				if condaEnv == "" {
					condaEnv = cfg.CondaEnv
				}
				if !cmd.Flags().Changed("gpu-index") {
					if cfg.DefaultGPU != nil {
						gpuIndex = *cfg.DefaultGPU
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
		Short: "Push local files to a resource with rsync",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, exec, cleanup, err := syncResourceExecutor(resourceName)
			if err != nil {
				return err
			}
			defer cleanup()
			source := expandPath(args[0])
			target := resolveSyncRemotePath(res, args[1])
			resolvedExcludes, excludeSources, err := resolveSyncExcludes(source, profile, noDefaultExcludes, excludes)
			if err != nil {
				return err
			}
			if !dryRun {
				if _, err := osexec.LookPath("rsync"); err != nil {
					return fmt.Errorf("local rsync not found: %w", err)
				}
				if err := checkRemoteRsyncViaExec(cmd.Context(), exec, res, 20); err != nil {
					return fmt.Errorf("remote rsync missing. Install rsync on %s or use remote-pull from a source with rsync: %w", res.Name, err)
				}
				if err := ensureRemoteDir(cmd.Context(), exec, res, target); err != nil {
					return err
				}
			}
			rsyncArgs := buildRsyncArgs(res, dryRun, deleteExtra, resolvedExcludes, extraArgs)
			rsyncArgs = append(rsyncArgs, source, remoteRsyncSpec(res, target))
			if dryRun {
				printSyncDryRunExcludes(profile, resolvedExcludes, excludeSources)
				fmt.Println(joinShellArgs(append([]string{"rsync"}, rsyncArgs...)))
				return nil
			}
			return runLocalRsync(cmd.Context(), timeoutSec, rsyncArgs)
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
	case "code", "code-data":
		return append([]string(nil), defaultSyncExcludes...), nil
	default:
		return nil, fmt.Errorf("unknown sync profile %q (expected code, code-data, or all)", profile)
	}
}

var defaultSyncExcludes = []string{
	".venv/",
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
	return joinShellArgs(parts)
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

func joinShellArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, cliShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// --- event ---

type eventOptions struct {
	path   string
	strict bool
	fields []string
}

func eventCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "event",
		Aliases: []string{"events"},
		Short:   "Emit structured UI events for an aexp run",
		Long: `Emit structured JSONL events to $AEXP_UI_EVENTS.

This is intended for training/setup scripts running inside an aexp run:

  aexp event metric train/loss 0.23 --epoch 3
  aexp event progress train 30 --total 100
  aexp event note "finished validation"

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
			event, err := eventFromFields(opts.fields)
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
	cmd.Flags().StringVar(&epoch, "epoch", "", "Epoch value")
	cmd.Flags().StringVar(&step, "step", "", "Step value")
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
			event, err := eventFromFields(opts.fields)
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
			event, err := eventFromFields(opts.fields)
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
			event, err := eventFromFields(opts.fields)
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
}

func eventFromFields(fields []string) (map[string]interface{}, error) {
	event := make(map[string]interface{}, len(fields)+4)
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("--field must be key=value, got %q", field)
		}
		event[key] = parseEventValue(value)
	}
	return event, nil
}

func emitStructuredEvent(opts eventOptions, event map[string]interface{}) error {
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
		return enc.Encode(event)
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
	return enc.Encode(event)
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
	cmd.AddCommand(runRefreshCmd())
	cmd.AddCommand(runLogsCmd())
	cmd.AddCommand(runCancelCmd())
	cmd.AddCommand(runArchiveCmd())
	cmd.AddCommand(runRestoreCmd())
	cmd.AddCommand(runDeleteCmd())
	cmd.AddCommand(runMarkCmd())
	cmd.AddCommand(runMarksCmd())

	return cmd
}

func runSubmitCmd() *cobra.Command {
	var resource, name, cwd, condaEnv, projectEnv, kind, uiEventsPath string
	var gpuIndex int
	var shellMode, force, noGPU, refreshEnv bool
	var logPaths, artifactPaths, metricPaths []string
	var launchTimeoutSec int

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
			submitReq.Cwd = cwd
			submitReq.CondaEnv = condaEnv
			submitReq.ProjectEnv = projectEnv
			submitReq.LogPaths = logPaths
			submitReq.ArtifactPaths = artifactPaths
			submitReq.MetricPaths = metricPaths
			submitReq.UIEventsPath = uiEventsPath
			submitReq.RefreshProjectEnv = refreshEnv

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

			launchCtx := cmd.Context()
			var cancel context.CancelFunc
			if launchTimeoutSec > 0 {
				launchCtx, cancel = context.WithTimeout(cmd.Context(), time.Duration(launchTimeoutSec)*time.Second)
				defer cancel()
			}
			createdID := ""
			run, err := exec.SubmitWithOptions(launchCtx, submitReq, executor.SubmitOptions{
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
			fmt.Printf("After inspection, record important findings with:\n  aexp run mark %s --title \"...\" --reason \"...\" --evidence \"logs/...\"\n", run.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "Resource name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Run name")
	cmd.Flags().StringVar(&kind, "kind", "formal", "Run kind: setup, smoke, pilot, formal, ablation")
	cmd.Flags().IntVar(&gpuIndex, "gpu-index", store.GPUIndexAll, "GPU index to use (-1 for all)")
	cmd.Flags().BoolVar(&noGPU, "no-gpu", false, "Do not reserve GPUs or set CUDA_VISIBLE_DEVICES")
	cmd.Flags().BoolVar(&shellMode, "shell", false, "Shell mode: interpret command via bash -lc")
	cmd.Flags().BoolVar(&force, "force", false, "Skip GPU slot lock, allow concurrent runs on same resource/GPU")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	cmd.Flags().StringVar(&condaEnv, "conda-env", "", "Conda environment")
	cmd.Flags().StringVar(&projectEnv, "project-env", "", "Runtime env strategy: auto or raw")
	cmd.Flags().BoolVar(&refreshEnv, "refresh-env", false, "Ignore cached project profile and re-detect the environment")
	cmd.Flags().StringSliceVar(&logPaths, "log-paths", nil, "Log file globs")
	cmd.Flags().StringSliceVar(&artifactPaths, "artifact-paths", nil, "Artifact file globs")
	cmd.Flags().StringSliceVar(&metricPaths, "metric-paths", nil, "Metric file globs")
	cmd.Flags().StringVar(&uiEventsPath, "ui-events", "", "Structured UI event JSONL file (default .aexp/events/<run_id>.jsonl; set off to disable)")
	cmd.Flags().IntVar(&launchTimeoutSec, "launch-timeout", 60, "Timeout in seconds for remote launch after the run record is created (0 = no timeout)")

	return cmd
}

func runListCmd() *cobra.Command {
	var status, resource string
	var asJSON bool
	var noRefresh bool
	var trash, deleted bool
	var refreshTimeoutSec int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()

			filter := store.RunFilter{Status: status, Limit: 50, Trash: trash, Deleted: deleted}
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
	cmd.Flags().BoolVar(&trash, "trash", false, "List runs in trash")
	cmd.Flags().BoolVar(&deleted, "deleted", false, "List logically deleted runs")
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
	if run.ExitCode.Valid {
		out["exit_code"] = run.ExitCode.Int64
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
	if run.ResolvedEnv != "" {
		fmt.Printf("resolved_env:    %s\n", run.ResolvedEnv)
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
	return run.Status == store.RunStatusRunning || run.Status == store.RunStatusStarting
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
		Use:   "marks [run_id]",
		Short: "List run findings",
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
