package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultProtocolVersion = "2024-11-05"
	maxToolOutputBytes     = 256 * 1024
)

type Server struct {
	binary string
}

type toolSpec struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
	Handler     func(*Server, context.Context, map[string]interface{}) (string, error)
}

func NewServer(binary string) *Server {
	return &Server{binary: binary}
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcErrorResponse(nil, -32700, "Parse error", err.Error()))
			continue
		}
		if len(req.ID) == 0 {
			s.handleNotification(req)
			continue
		}

		resp := s.handleRequest(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleNotification(req rpcRequest) {
	// MCP notifications such as notifications/initialized intentionally do not
	// receive JSON-RPC responses.
	_ = req
}

func (s *Server) handleRequest(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResultResponse(req.ID, map[string]interface{}{
			"protocolVersion": defaultProtocolVersion,
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "aexp",
				"version": "dev",
			},
		})
	case "ping":
		return rpcResultResponse(req.ID, map[string]interface{}{})
	case "tools/list":
		return rpcResultResponse(req.ID, map[string]interface{}{"tools": toolDefinitions()})
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcErrorResponse(req.ID, -32602, "Invalid params", err.Error())
		}
		result, err := s.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			return rpcResultResponse(req.ID, toolTextResult(err.Error(), true))
		}
		return rpcResultResponse(req.ID, toolTextResult(result, false))
	default:
		return rpcErrorResponse(req.ID, -32601, "Method not found", req.Method)
	}
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	for _, tool := range toolRegistry() {
		if tool.Name == name {
			return tool.Handler(s, ctx, args)
		}
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (s *Server) toolExec(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	command, err := requiredString(args, "command")
	if err != nil {
		return "", err
	}

	timeoutSec := intArg(args, "timeout", 30)
	cli := []string{"exec", "--json", "--resource", resource, "--timeout", strconv.Itoa(timeoutSec)}
	if v := stringArg(args, "api", ""); v != "" {
		cli = append(cli, "--api", v)
	}
	if v := stringArg(args, "cwd", ""); v != "" {
		cli = append(cli, "--cwd", v)
	}
	if v := stringArg(args, "project_env", ""); v != "" {
		cli = append(cli, "--project-env", v)
	}
	if v := stringArg(args, "conda_env", ""); v != "" {
		cli = append(cli, "--conda-env", v)
	}
	if boolArg(args, "shell", false) {
		cli = append(cli, "--shell")
	}
	if boolArg(args, "force", false) {
		cli = append(cli, "--force")
	}
	if boolArg(args, "direct", false) {
		cli = append(cli, "--direct")
	}
	if boolArg(args, "refresh_env", false) {
		cli = append(cli, "--refresh-env")
	}
	if boolArg(args, "dry_run", false) {
		cli = append(cli, "--dry-run")
	}
	cli = append(cli, "--", command)
	return s.runAexp(ctx, time.Duration(timeoutSec+15)*time.Second, cli...)
}

func (s *Server) toolSubmitRun(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}

	cli := []string{"run", "submit", "--resource", resource}
	if v := stringArg(args, "name", ""); v != "" {
		cli = append(cli, "--name", v)
	}
	if v := stringArg(args, "kind", ""); v != "" {
		cli = append(cli, "--kind", v)
	}
	if v := stringArg(args, "cwd", ""); v != "" {
		cli = append(cli, "--cwd", v)
	}
	if v := stringArg(args, "project_env", ""); v != "" {
		cli = append(cli, "--project-env", v)
	}
	if v := stringArg(args, "conda_env", ""); v != "" {
		cli = append(cli, "--conda-env", v)
	}
	if v, ok := optionalIntArg(args, "gpu_index"); ok {
		cli = append(cli, "--gpu-index", strconv.Itoa(v))
	}
	for _, p := range stringSliceArg(args, "log_paths") {
		cli = append(cli, "--log-paths", p)
	}
	for _, p := range stringSliceArg(args, "metric_paths") {
		cli = append(cli, "--metric-paths", p)
	}
	for _, p := range stringSliceArg(args, "artifact_paths") {
		cli = append(cli, "--artifact-paths", p)
	}
	if v := stringArg(args, "ui_events", ""); v != "" {
		cli = append(cli, "--ui-events", v)
	}
	submitShell := boolArg(args, "shell", false)
	if submitShell {
		cli = append(cli, "--shell")
	}
	if boolArg(args, "force", false) {
		cli = append(cli, "--force")
	}
	if boolArg(args, "no_gpu", false) {
		cli = append(cli, "--no-gpu")
	}
	if boolArg(args, "refresh_env", false) {
		cli = append(cli, "--refresh-env")
	}
	if v, ok := optionalIntArg(args, "launch_timeout"); ok {
		cli = append(cli, "--launch-timeout", strconv.Itoa(v))
	}

	var runArgs []string
	if argv := stringSliceArg(args, "argv"); len(argv) > 0 {
		runArgs = argv
	} else {
		command, err := requiredString(args, "command")
		if err != nil {
			return "", errors.New("command or argv is required")
		}
		if !submitShell {
			cli = append(cli, "--shell")
		}
		runArgs = []string{command}
	}
	cli = append(cli, "--")
	cli = append(cli, runArgs...)

	output, err := s.runAexp(ctx, timeoutFromArgs(args, 90), cli...)
	if err != nil {
		return output, err
	}
	runID := firstRunID(output)
	if runID == "" {
		return output, nil
	}
	status, statusErr := s.runAexp(ctx, 20*time.Second, "run", "status", runID, "--short", "--json")
	if statusErr != nil {
		return jsonText(map[string]interface{}{
			"run_id":        runID,
			"submit_output": output,
			"status_error":  statusErr.Error(),
		}), nil
	}
	return jsonText(map[string]interface{}{
		"run_id":        runID,
		"submit_output": output,
		"status":        json.RawMessage(status),
	}), nil
}

func (s *Server) toolEventMetric(ctx context.Context, args map[string]interface{}) (string, error) {
	name, err := requiredString(args, "name")
	if err != nil {
		return "", err
	}
	value := numberStringArg(args, "value")
	if value == "" {
		var err error
		value, err = requiredString(args, "value")
		if err != nil {
			return "", err
		}
	}
	cli := []string{"event", "metric", name, value}
	addEventCommonFlags(&cli, args)
	if v := numberStringArg(args, "epoch"); v != "" {
		cli = append(cli, "--epoch", v)
	}
	if v := numberStringArg(args, "step"); v != "" {
		cli = append(cli, "--step", v)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEventProgress(ctx context.Context, args map[string]interface{}) (string, error) {
	name, err := requiredString(args, "name")
	if err != nil {
		return "", err
	}
	current := numberStringArg(args, "current")
	if current == "" {
		var err error
		current, err = requiredString(args, "current")
		if err != nil {
			return "", err
		}
	}
	cli := []string{"event", "progress", name, current}
	addEventCommonFlags(&cli, args)
	if v := numberStringArg(args, "total"); v != "" {
		cli = append(cli, "--total", v)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEventParam(ctx context.Context, args map[string]interface{}) (string, error) {
	name, err := requiredString(args, "name")
	if err != nil {
		return "", err
	}
	value, err := requiredString(args, "value")
	if err != nil {
		return "", err
	}
	cli := []string{"event", "param", name, value}
	addEventCommonFlags(&cli, args)
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEventNote(ctx context.Context, args map[string]interface{}) (string, error) {
	text, err := requiredString(args, "text")
	if err != nil {
		return "", err
	}
	cli := []string{"event", "note", text}
	addEventCommonFlags(&cli, args)
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolListRuns(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"run", "list", "--json"}
	if v := stringArg(args, "status", ""); v != "" {
		cli = append(cli, "--status", v)
	}
	if v := stringArg(args, "resource", ""); v != "" {
		cli = append(cli, "--resource", v)
	}
	if boolArg(args, "no_refresh", false) {
		cli = append(cli, "--no-refresh")
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolTailRunLogs(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	source := stringArg(args, "source", "stdout")
	if source == "" {
		source = "stdout"
	}
	last := intArg(args, "last", intArg(args, "last_n", 100))
	cli := []string{"run", "logs", runID, "--source", source, "--last", strconv.Itoa(last), "--no-follow"}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolCLI(ctx context.Context, args map[string]interface{}) (string, error) {
	cliArgs := stringSliceArg(args, "args")
	if len(cliArgs) == 0 {
		return "", errors.New("args is required")
	}
	if err := allowReadOnlyCLI(cliArgs); err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cliArgs...)
}

func (s *Server) toolInit(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"init"}
	addProjectInitFlags(&cli, args, true)
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolProjectInit(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"project", "init"}
	addProjectInitFlags(&cli, args, false)
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolMarkRun(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	cli := []string{"run", "mark", runID, "--json"}
	if v := stringArg(args, "actor", ""); v != "" {
		cli = append(cli, "--actor", v)
	}
	if v := stringArg(args, "kind", ""); v != "" {
		cli = append(cli, "--kind", v)
	}
	if v := stringArg(args, "title", ""); v != "" {
		cli = append(cli, "--title", v)
	}
	if v := stringArg(args, "reason", ""); v != "" {
		cli = append(cli, "--reason", v)
	}
	if v := stringArg(args, "evidence", ""); v != "" {
		cli = append(cli, "--evidence", v)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolListRunMarks(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"run", "marks", "--json"}
	if v := stringArg(args, "run_id", ""); v != "" {
		cli = append(cli, v)
	}
	if v := stringArg(args, "actor", ""); v != "" {
		cli = append(cli, "--actor", v)
	}
	if v := stringArg(args, "kind", ""); v != "" {
		cli = append(cli, "--kind", v)
	}
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolProjectDoctor(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"project", "doctor", "--json"}
	addOptionalStringFlag(&cli, args, "resource", "--resource")
	addOptionalStringFlag(&cli, args, "cwd", "--cwd")
	addOptionalStringFlag(&cli, args, "conda_env", "--conda-env")
	addOptionalStringFlag(&cli, args, "config", "--config")
	addOptionalStringFlag(&cli, args, "recipe", "--recipe")
	if v, ok := optionalIntArg(args, "gpu_index"); ok {
		cli = append(cli, "--gpu-index", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 60), cli...)
}

func (s *Server) toolProjectRun(ctx context.Context, args map[string]interface{}) (string, error) {
	name := stringArg(args, "recipe", stringArg(args, "name", ""))
	cli := []string{"project", "run"}
	if name != "" {
		cli = append(cli, name)
	}
	addOptionalStringFlag(&cli, args, "config", "--config")
	addOptionalStringFlag(&cli, args, "resource", "--resource")
	addOptionalStringFlag(&cli, args, "cwd", "--cwd")
	addOptionalStringFlag(&cli, args, "run_name", "--name")
	addOptionalStringFlag(&cli, args, "kind", "--kind")
	addOptionalStringFlag(&cli, args, "project_env", "--project-env")
	addOptionalStringFlag(&cli, args, "conda_env", "--conda-env")
	if v, ok := optionalIntArg(args, "gpu_index"); ok {
		cli = append(cli, "--gpu-index", strconv.Itoa(v))
	}
	addBoolFlag(&cli, args, "no_gpu", "--no-gpu")
	addBoolFlag(&cli, args, "force", "--force")
	addBoolFlag(&cli, args, "dry_run", "--dry-run")
	addBoolFlag(&cli, args, "refresh_env", "--refresh-env")
	if v, ok := optionalIntArg(args, "launch_timeout"); ok {
		cli = append(cli, "--launch-timeout", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 90), cli...)
}

func (s *Server) toolProjectSync(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"project", "sync"}
	addOptionalStringFlag(&cli, args, "config", "--config")
	addOptionalStringFlag(&cli, args, "resource", "--resource")
	addOptionalStringFlag(&cli, args, "source", "--source")
	addOptionalStringFlag(&cli, args, "target", "--target")
	addOptionalStringFlag(&cli, args, "profile", "--profile")
	addBoolFlag(&cli, args, "dry_run", "--dry-run")
	addBoolFlag(&cli, args, "delete", "--delete")
	addBoolFlag(&cli, args, "no_default_excludes", "--no-default-excludes")
	for _, p := range stringSliceArg(args, "exclude") {
		cli = append(cli, "--exclude", p)
	}
	if v, ok := optionalIntArg(args, "sync_timeout"); ok {
		cli = append(cli, "--timeout", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 120), cli...)
}

func (s *Server) toolSyncDoctor(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	cli := []string{"sync", "doctor", "--json", "--resource", resource}
	if v, ok := optionalIntArg(args, "check_timeout"); ok {
		cli = append(cli, "--timeout", strconv.Itoa(v))
	}
	if v := stringArg(args, "source", ""); v != "" {
		cli = append(cli, v)
	}
	if v := stringArg(args, "target", ""); v != "" {
		cli = append(cli, v)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 60), cli...)
}

func (s *Server) toolDoctor(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	cli := []string{"doctor", "--json", "--resource", resource}
	addOptionalStringFlag(&cli, args, "cwd", "--cwd")
	addOptionalStringFlag(&cli, args, "conda_env", "--conda-env")
	if v, ok := optionalIntArg(args, "gpu_index"); ok {
		cli = append(cli, "--gpu-index", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 60), cli...)
}

func (s *Server) toolResourceExplore(ctx context.Context, args map[string]interface{}) (string, error) {
	host, err := requiredString(args, "host")
	if err != nil {
		return "", err
	}
	cli := []string{"resource", "explore", host, "--json"}
	addOptionalStringFlag(&cli, args, "user", "--user")
	addOptionalStringFlag(&cli, args, "key", "--key")
	addOptionalStringFlag(&cli, args, "auth_ref", "--auth-ref")
	addOptionalStringFlag(&cli, args, "socks_proxy", "--socks-proxy")
	addOptionalStringFlag(&cli, args, "proxy_command", "--proxy-command")
	if v, ok := optionalIntArg(args, "port"); ok {
		cli = append(cli, "--port", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 60), cli...)
}

func (s *Server) toolResourceAdd(ctx context.Context, args map[string]interface{}) (string, error) {
	name, err := requiredString(args, "name")
	if err != nil {
		return "", err
	}
	host, err := requiredString(args, "host")
	if err != nil {
		return "", err
	}
	rootDir, err := requiredString(args, "root_dir")
	if err != nil {
		return "", err
	}
	cli := []string{"resource", "add", "--name", name, "--host", host, "--root-dir", rootDir}
	addResourceOptionalFlags(&cli, args, false)
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolResourceAddLocal(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"resource", "add-local"}
	addOptionalStringFlag(&cli, args, "name", "--name")
	addOptionalStringFlag(&cli, args, "root_dir", "--root-dir")
	addBoolFlag(&cli, args, "skip_test", "--skip-test")
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolResourceUpdate(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	cli := []string{"resource", "update", resource}
	addOptionalStringFlag(&cli, args, "host", "--host")
	addResourceOptionalFlags(&cli, args, true)
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolResourceRemove(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), "resource", "remove", resource)
}

func (s *Server) toolRunRefresh(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"run", "refresh", "--json"}
	if runID := stringArg(args, "run_id", ""); runID != "" {
		cli = append(cli, runID)
	}
	addOptionalStringFlag(&cli, args, "resource", "--resource")
	if v, ok := optionalIntArg(args, "refresh_timeout"); ok {
		cli = append(cli, "--timeout", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 60), cli...)
}

func (s *Server) toolExecHistory(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"exec", "history", "--json"}
	addOptionalStringFlag(&cli, args, "resource", "--resource")
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolExecShow(ctx context.Context, args map[string]interface{}) (string, error) {
	eventID, err := requiredString(args, "event_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "exec", "show", eventID, "--json")
}

func (s *Server) toolProjectDetect(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	cli := []string{"project", "detect", "--json", "--resource", resource}
	addOptionalStringFlag(&cli, args, "cwd", "--cwd")
	addOptionalStringFlag(&cli, args, "project_env", "--project-env")
	addOptionalStringFlag(&cli, args, "conda_env", "--conda-env")
	return s.runAexp(ctx, timeoutFromArgs(args, 60), cli...)
}

func (s *Server) toolSyncPush(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, source, target, err := syncEndpointArgs(args, "source", "target")
	if err != nil {
		return "", err
	}
	cli := []string{"sync", "push", "--resource", resource}
	addSyncFlags(&cli, args, true)
	cli = append(cli, source, target)
	return s.runAexp(ctx, timeoutFromArgs(args, 120), cli...)
}

func (s *Server) toolSyncPull(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, source, target, err := syncEndpointArgs(args, "source", "target")
	if err != nil {
		return "", err
	}
	cli := []string{"sync", "pull", "--resource", resource}
	addBoolFlag(&cli, args, "dry_run", "--dry-run")
	addBoolFlag(&cli, args, "delete", "--delete")
	for _, p := range stringSliceArg(args, "exclude") {
		cli = append(cli, "--exclude", p)
	}
	for _, p := range stringSliceArg(args, "rsync_arg") {
		cli = append(cli, "--rsync-arg", p)
	}
	if v, ok := optionalIntArg(args, "sync_timeout"); ok {
		cli = append(cli, "--timeout", strconv.Itoa(v))
	}
	cli = append(cli, source, target)
	return s.runAexp(ctx, timeoutFromArgs(args, 120), cli...)
}

func (s *Server) toolSyncRemotePull(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, source, target, err := syncEndpointArgs(args, "source", "target")
	if err != nil {
		return "", err
	}
	cli := []string{"sync", "remote-pull", "--resource", resource}
	addSyncFlags(&cli, args, true)
	cli = append(cli, source, target)
	return s.runAexp(ctx, timeoutFromArgs(args, 120), cli...)
}

func syncEndpointArgs(args map[string]interface{}, sourceKey string, targetKey string) (string, string, string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", "", "", err
	}
	source, err := requiredString(args, sourceKey)
	if err != nil {
		return "", "", "", err
	}
	target, err := requiredString(args, targetKey)
	if err != nil {
		return "", "", "", err
	}
	return resource, source, target, nil
}

func addOptionalStringFlag(cli *[]string, args map[string]interface{}, key string, flag string) {
	if v := stringArg(args, key, ""); v != "" {
		*cli = append(*cli, flag, v)
	}
}

func addBoolFlag(cli *[]string, args map[string]interface{}, key string, flag string) {
	if boolArg(args, key, false) {
		*cli = append(*cli, flag)
	}
}

func addProjectInitFlags(cli *[]string, args map[string]interface{}, includeProjectFlag bool) {
	if includeProjectFlag && boolArg(args, "project", false) {
		*cli = append(*cli, "--project")
	}
	addOptionalStringFlag(cli, args, "resource", "--resource")
	addOptionalStringFlag(cli, args, "cwd", "--cwd")
	addOptionalStringFlag(cli, args, "env", "--env")
	addOptionalStringFlag(cli, args, "conda_env", "--conda-env")
	addOptionalStringFlag(cli, args, "output", "--output")
	if v, ok := optionalIntArg(args, "default_gpu"); ok {
		*cli = append(*cli, "--default-gpu", strconv.Itoa(v))
	}
	addBoolFlag(cli, args, "force", "--force")
	addBoolFlag(cli, args, "dry_run", "--dry-run")
	addBoolFlag(cli, args, "no_events_helper", "--no-events-helper")
}

func addResourceOptionalFlags(cli *[]string, args map[string]interface{}, includeName bool) {
	if includeName {
		addOptionalStringFlag(cli, args, "name", "--name")
	}
	addOptionalStringFlag(cli, args, "type", "--type")
	addOptionalStringFlag(cli, args, "os_type", "--os-type")
	addOptionalStringFlag(cli, args, "user", "--user")
	addOptionalStringFlag(cli, args, "auth_ref", "--auth-ref")
	addOptionalStringFlag(cli, args, "conda_base", "--conda-base")
	addOptionalStringFlag(cli, args, "conda_init", "--conda-init")
	addOptionalStringFlag(cli, args, "conda_env", "--conda-env")
	addOptionalStringFlag(cli, args, "gpu_indices", "--gpu-indices")
	addOptionalStringFlag(cli, args, "tags", "--tags")
	addOptionalStringFlag(cli, args, "socks_proxy", "--socks-proxy")
	addOptionalStringFlag(cli, args, "proxy_command", "--proxy-command")
	if v := stringArg(args, "root_dir", ""); v != "" && includeName {
		*cli = append(*cli, "--root-dir", v)
	}
	if v, ok := optionalIntArg(args, "port"); ok {
		*cli = append(*cli, "--port", strconv.Itoa(v))
	}
}

func addSyncFlags(cli *[]string, args map[string]interface{}, includeProfile bool) {
	addBoolFlag(cli, args, "dry_run", "--dry-run")
	addBoolFlag(cli, args, "delete", "--delete")
	addBoolFlag(cli, args, "no_default_excludes", "--no-default-excludes")
	if includeProfile {
		addOptionalStringFlag(cli, args, "profile", "--profile")
	}
	for _, p := range stringSliceArg(args, "exclude") {
		*cli = append(*cli, "--exclude", p)
	}
	for _, p := range stringSliceArg(args, "rsync_arg") {
		*cli = append(*cli, "--rsync-arg", p)
	}
	if v, ok := optionalIntArg(args, "sync_timeout"); ok {
		*cli = append(*cli, "--timeout", strconv.Itoa(v))
	}
}

func addEventCommonFlags(cli *[]string, args map[string]interface{}) {
	addOptionalStringFlag(cli, args, "path", "--path")
	addBoolFlag(cli, args, "strict", "--strict")
	for _, field := range stringSliceArg(args, "field") {
		*cli = append(*cli, "--field", field)
	}
	for k, v := range stringMapArg(args, "fields") {
		*cli = append(*cli, "--field", k+"="+v)
	}
}

func allowReadOnlyCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("args is required")
	}
	if strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("first arg must be a command, got %q", args[0])
	}
	if len(args) == 1 {
		switch args[0] {
		case "agent":
			return nil
		default:
			return fmt.Errorf("aexp_cli does not allow %q", strings.Join(args, " "))
		}
	}
	allowed := map[string]bool{
		"doctor":           true,
		"resource list":    true,
		"resource explore": true,
		"run list":         true,
		"run status":       true,
		"run logs":         true,
		"run marks":        true,
		"exec history":     true,
		"exec show":        true,
		"project detect":   true,
		"project doctor":   true,
		"sync doctor":      true,
	}
	key := args[0] + " " + args[1]
	if !allowed[key] {
		return fmt.Errorf("aexp_cli only allows read-only commands; %q is not allowed", key)
	}
	for _, arg := range args {
		if arg == "--follow" {
			return fmt.Errorf("aexp_cli does not allow unbounded --follow; use aexp_tail_run_logs")
		}
	}
	return nil
}

func (s *Server) runAexp(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, s.binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	errText := stderr.String()
	if runCtx.Err() == context.DeadlineExceeded {
		return truncateOutput(out, errText), fmt.Errorf("aexp %s timed out after %s", strings.Join(args, " "), timeout)
	}
	if err != nil {
		if errText != "" {
			return truncateOutput(out, errText), fmt.Errorf("aexp %s failed: %s", strings.Join(args, " "), strings.TrimSpace(errText))
		}
		return truncateOutput(out, errText), fmt.Errorf("aexp %s failed: %w", strings.Join(args, " "), err)
	}
	return truncateOutput(out, errText), nil
}

func truncateOutput(stdout string, stderr string) string {
	out := stdout
	if stderr != "" {
		if out != "" {
			out += "\n"
		}
		out += "[stderr]\n" + stderr
	}
	if len(out) <= maxToolOutputBytes {
		return out
	}
	return out[:maxToolOutputBytes] + "\n...[truncated by aexp mcp]..."
}

var runIDPattern = regexp.MustCompile(`run_[0-9A-Za-z]+`)

func firstRunID(s string) string {
	return runIDPattern.FindString(s)
}

func jsonText(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func rpcResultResponse(id json.RawMessage, result interface{}) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcErrorResponse(id json.RawMessage, code int, message string, data interface{}) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}}
}

func toolTextResult(text string, isError bool) map[string]interface{} {
	return map[string]interface{}{
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"isError": isError,
	}
}

func requiredString(args map[string]interface{}, key string) (string, error) {
	v := stringArg(args, key, "")
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func stringArg(args map[string]interface{}, key string, def string) string {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return def
}

func numberStringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func boolArg(args map[string]interface{}, key string, def bool) bool {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func intArg(args map[string]interface{}, key string, def int) int {
	if v, ok := optionalIntArg(args, key); ok {
		return v
	}
	return def
}

func optionalIntArg(args map[string]interface{}, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		i, err := strconv.Atoi(v.String())
		return i, err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		return i, err == nil
	default:
		return 0, false
	}
}

func timeoutFromArgs(args map[string]interface{}, defSec int) time.Duration {
	sec := intArg(args, "timeout", defSec)
	if sec <= 0 {
		sec = defSec
	}
	return time.Duration(sec) * time.Second
}

func stringSliceArg(args map[string]interface{}, key string) []string {
	if args == nil {
		return nil
	}
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch raw := v.(type) {
	case []string:
		return raw
	case []interface{}:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		return []string{strings.TrimSpace(raw)}
	default:
		return nil
	}
}

func stringMapArg(args map[string]interface{}, key string) map[string]string {
	if args == nil {
		return nil
	}
	raw, ok := args[key]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		switch typed := v.(type) {
		case string:
			out[k] = typed
		case float64:
			out[k] = strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(typed)
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", typed)
		}
	}
	return out
}

func toolDefinitions() []map[string]interface{} {
	tools := toolRegistry()
	out := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
	}
	return out
}

func toolRegistry() []toolSpec {
	return []toolSpec{
		{
			Name:        "aexp_agent_card",
			Description: "Return the short aexp operating guide for agents.",
			InputSchema: objectSchema(map[string]interface{}{}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.runAexp(ctx, 10*time.Second, "agent", "--json")
			},
		},
		{
			Name:        "aexp_init",
			Description: "Initialize local aexp state and optionally create a project config.",
			InputSchema: objectSchema(projectInitSchema(map[string]interface{}{
				"project": boolSchema("Also create a project .aexp.yaml in the current directory."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}), nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolInit(ctx, args)
			},
		},
		{
			Name:        "aexp_project_init",
			Description: "Create a project .aexp.yaml recipe file.",
			InputSchema: objectSchema(projectInitSchema(map[string]interface{}{
				"timeout": numberSchema("Tool timeout in seconds."),
			}), nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectInit(ctx, args)
			},
		},
		{
			Name:        "aexp_list_resources",
			Description: "List configured aexp resources.",
			InputSchema: objectSchema(map[string]interface{}{
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.runAexp(ctx, timeoutFromArgs(args, 10), "resource", "list", "--json")
			},
		},
		{
			Name:        "aexp_doctor",
			Description: "Check whether a resource/cwd/conda environment is ready for runs.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":  stringSchema("Resource name."),
				"cwd":       stringSchema("Project working directory."),
				"conda_env": stringSchema("Conda environment override."),
				"gpu_index": numberSchema("GPU index used in the recommended submit command."),
				"timeout":   numberSchema("Tool timeout in seconds."),
			}, []string{"resource"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolDoctor(ctx, args)
			},
		},
		{
			Name:        "aexp_resource_explore",
			Description: "Discover environment on a remote host before registering it.",
			InputSchema: objectSchema(resourceConnectionSchema(map[string]interface{}{
				"host":    stringSchema("SSH host."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}), []string{"host"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolResourceExplore(ctx, args)
			},
		},
		{
			Name:        "aexp_resource_add",
			Description: "Register a new SSH resource.",
			InputSchema: objectSchema(resourceConfigSchema(map[string]interface{}{
				"name":     stringSchema("Resource name."),
				"host":     stringSchema("SSH host."),
				"root_dir": stringSchema("Workspace root directory."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}), []string{"name", "host", "root_dir"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolResourceAdd(ctx, args)
			},
		},
		{
			Name:        "aexp_resource_add_local",
			Description: "Register this machine as a localhost SSH resource.",
			InputSchema: objectSchema(map[string]interface{}{
				"name":      stringSchema("Resource name, defaults to local hostname."),
				"root_dir":  stringSchema("Workspace root directory, defaults to current directory."),
				"skip_test": boolSchema("Register without testing ssh localhost first."),
				"timeout":   numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolResourceAddLocal(ctx, args)
			},
		},
		{
			Name:        "aexp_resource_update",
			Description: "Update an existing resource.",
			InputSchema: objectSchema(resourceConfigSchema(map[string]interface{}{
				"resource": stringSchema("Existing resource name."),
				"name":     stringSchema("New resource name."),
				"host":     stringSchema("New SSH host."),
				"root_dir": stringSchema("New workspace root directory."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}), []string{"resource"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolResourceUpdate(ctx, args)
			},
		},
		{
			Name:        "aexp_resource_remove",
			Description: "Remove a registered resource.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource": stringSchema("Resource name."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, []string{"resource"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolResourceRemove(ctx, args)
			},
		},
		{
			Name:        "aexp_exec",
			Description: "Run a short bounded command on a resource. This uses the aexp exec fast path: local API if available, direct SSH fallback otherwise.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":    stringSchema("Resource name."),
				"command":     stringSchema("Remote command string."),
				"api":         stringSchema("Local aexp API base URL for exec fast path."),
				"cwd":         stringSchema("Optional remote working directory."),
				"project_env": stringSchema("Optional runtime strategy: auto or raw."),
				"conda_env":   stringSchema("Optional conda environment override."),
				"timeout":     numberSchema("Command timeout in seconds."),
				"shell":       boolSchema("Join command through shell mode."),
				"force":       boolSchema("Allow command even if aexp detects a long-running pattern."),
				"dry_run":     boolSchema("Preview command and long-running check without executing."),
				"direct":      boolSchema("Skip local aexp API fast path."),
				"refresh_env": boolSchema("Ignore cached project env detection."),
			}, []string{"resource", "command"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolExec(ctx, args)
			},
		},
		{
			Name:        "aexp_submit_run",
			Description: "Submit a long-running tracked run. Use this for setup, smoke, pilot, formal, and ablation jobs.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":       stringSchema("Resource name."),
				"command":        stringSchema("Shell command string. Alternative to argv."),
				"argv":           arrayStringSchema("Structured argv. Alternative to command."),
				"name":           stringSchema("Run name."),
				"kind":           stringSchema("Run kind: setup, smoke, pilot, formal, ablation."),
				"cwd":            stringSchema("Remote working directory."),
				"project_env":    stringSchema("Optional runtime strategy: auto or raw."),
				"conda_env":      stringSchema("Optional conda environment."),
				"gpu_index":      numberSchema("GPU index. -1 means all, -2 means none."),
				"no_gpu":         boolSchema("Do not reserve GPUs or set CUDA_VISIBLE_DEVICES."),
				"shell":          boolSchema("Interpret argv through bash -lc."),
				"force":          boolSchema("Skip GPU lock."),
				"refresh_env":    boolSchema("Ignore cached project env detection."),
				"log_paths":      arrayStringSchema("Log file globs."),
				"metric_paths":   arrayStringSchema("Metric file globs."),
				"artifact_paths": arrayStringSchema("Artifact file globs."),
				"ui_events":      stringSchema("Structured UI event JSONL path, or off."),
				"launch_timeout": numberSchema("Timeout in seconds for remote launch after the run record is created."),
				"timeout":        numberSchema("MCP tool timeout in seconds."),
			}, []string{"resource"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSubmitRun(ctx, args)
			},
		},
		{
			Name:        "aexp_list_runs",
			Description: "List recent aexp runs as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"status":     stringSchema("Optional status filter."),
				"resource":   stringSchema("Optional resource name filter."),
				"no_refresh": boolSchema("Avoid refreshing running runs."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolListRuns(ctx, args)
			},
		},
		{
			Name:        "aexp_refresh_runs",
			Description: "Refresh one run or all active runs from remote state.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":          stringSchema("Optional run id."),
				"resource":        stringSchema("Optional resource name."),
				"refresh_timeout": numberSchema("Timeout per running/starting status refresh in seconds."),
				"timeout":         numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunRefresh(ctx, args)
			},
		},
		{
			Name:        "aexp_get_run_status",
			Description: "Get one run's refreshed short status as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 20), "run", "status", runID, "--short", "--json")
			},
		},
		{
			Name:        "aexp_tail_run_logs",
			Description: "Read the latest log snapshot for a run. This is a bounded snapshot, not an endless follow stream.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"source":  stringSchema("stdout or stderr."),
				"last":    numberSchema("Number of lines to read."),
				"last_n":  numberSchema("Alias for last."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolTailRunLogs(ctx, args)
			},
		},
		{
			Name:        "aexp_cancel_run",
			Description: "Cancel a running aexp run.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 20), "run", "cancel", runID)
			},
		},
		{
			Name:        "aexp_mark_run",
			Description: "Attach an agent/human finding to a run.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":   stringSchema("Run id."),
				"actor":    stringSchema("Actor writing the mark."),
				"kind":     stringSchema("Mark kind, e.g. key_result, failure, note, followup."),
				"title":    stringSchema("Short title for the finding."),
				"reason":   stringSchema("Why this run matters."),
				"evidence": stringSchema("Lightweight Markdown/plain-text evidence, log paths, or artifact paths."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolMarkRun(ctx, args)
			},
		},
		{
			Name:        "aexp_list_run_marks",
			Description: "List run findings/marks as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Optional run id."),
				"actor":   stringSchema("Optional actor filter."),
				"kind":    stringSchema("Optional mark kind filter."),
				"limit":   numberSchema("Max marks to show."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolListRunMarks(ctx, args)
			},
		},
		{
			Name:        "aexp_exec_history",
			Description: "Show recent one-shot exec event history as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource": stringSchema("Optional resource name."),
				"actor":    stringSchema("Optional actor filter."),
				"limit":    numberSchema("Max events to show."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolExecHistory(ctx, args)
			},
		},
		{
			Name:        "aexp_exec_show",
			Description: "Show details of a specific one-shot exec event as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"event_id": stringSchema("Exec event id."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, []string{"event_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolExecShow(ctx, args)
			},
		},
		{
			Name:        "aexp_event_metric",
			Description: "Emit a numeric metric event to a structured UI event JSONL file.",
			InputSchema: objectSchema(eventSchema(map[string]interface{}{
				"name":    stringSchema("Metric name, e.g. train/loss."),
				"value":   stringSchema("Numeric metric value."),
				"epoch":   numberSchema("Optional epoch value."),
				"step":    numberSchema("Optional step value."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}), []string{"name", "value"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEventMetric(ctx, args)
			},
		},
		{
			Name:        "aexp_event_progress",
			Description: "Emit a progress event to a structured UI event JSONL file.",
			InputSchema: objectSchema(eventSchema(map[string]interface{}{
				"name":    stringSchema("Progress name, e.g. epoch."),
				"current": stringSchema("Current progress value."),
				"total":   numberSchema("Optional total progress value."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}), []string{"name", "current"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEventProgress(ctx, args)
			},
		},
		{
			Name:        "aexp_event_param",
			Description: "Emit a run parameter event to a structured UI event JSONL file.",
			InputSchema: objectSchema(eventSchema(map[string]interface{}{
				"name":    stringSchema("Parameter name."),
				"value":   stringSchema("Parameter value."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}), []string{"name", "value"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEventParam(ctx, args)
			},
		},
		{
			Name:        "aexp_event_note",
			Description: "Emit a note event to a structured UI event JSONL file.",
			InputSchema: objectSchema(eventSchema(map[string]interface{}{
				"text":    stringSchema("Note text."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}), []string{"text"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEventNote(ctx, args)
			},
		},
		{
			Name:        "aexp_project_detect",
			Description: "Detect a project runtime profile and save it locally.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":    stringSchema("Resource name."),
				"cwd":         stringSchema("Project working directory."),
				"project_env": stringSchema("Runtime env strategy: auto or raw."),
				"conda_env":   stringSchema("Conda environment override."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, []string{"resource"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectDetect(ctx, args)
			},
		},
		{
			Name:        "aexp_project_doctor",
			Description: "Validate project config/runtime and return the recommended next command as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":  stringSchema("Optional resource name override."),
				"cwd":       stringSchema("Optional project working directory."),
				"conda_env": stringSchema("Optional conda environment override."),
				"config":    stringSchema("Optional project config path."),
				"recipe":    stringSchema("Optional recipe to validate."),
				"gpu_index": numberSchema("GPU index used in recommendation."),
				"timeout":   numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectDoctor(ctx, args)
			},
		},
		{
			Name:        "aexp_project_run",
			Description: "Submit a configured project recipe from .aexp.yaml.",
			InputSchema: objectSchema(map[string]interface{}{
				"recipe":         stringSchema("Project recipe name, defaults to train."),
				"name":           stringSchema("Alias for recipe."),
				"run_name":       stringSchema("Override run name."),
				"config":         stringSchema("Optional project config path."),
				"resource":       stringSchema("Override resource name."),
				"cwd":            stringSchema("Override working directory."),
				"kind":           stringSchema("Override run kind."),
				"project_env":    stringSchema("Override runtime env strategy: auto or raw."),
				"conda_env":      stringSchema("Override conda environment."),
				"gpu_index":      numberSchema("Override GPU index."),
				"no_gpu":         boolSchema("Do not reserve GPUs or set CUDA_VISIBLE_DEVICES."),
				"force":          boolSchema("Skip GPU slot lock."),
				"dry_run":        boolSchema("Print the resolved submit command without launching."),
				"refresh_env":    boolSchema("Ignore cached project profile and re-detect the environment."),
				"launch_timeout": numberSchema("Launch timeout in seconds."),
				"timeout":        numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectRun(ctx, args)
			},
		},
		{
			Name:        "aexp_project_sync",
			Description: "Push files using sync settings from .aexp.yaml.",
			InputSchema: objectSchema(map[string]interface{}{
				"config":              stringSchema("Optional project config path."),
				"resource":            stringSchema("Override resource name."),
				"source":              stringSchema("Override local source."),
				"target":              stringSchema("Override remote target."),
				"profile":             stringSchema("Override exclude profile: code, code-data, all."),
				"dry_run":             boolSchema("Print rsync command without running it."),
				"delete":              boolSchema("Delete target files that no longer exist on source."),
				"no_default_excludes": boolSchema("Disable profile excludes and .aexpignore."),
				"exclude":             arrayStringSchema("Extra exclude patterns."),
				"sync_timeout":        numberSchema("Rsync timeout in seconds."),
				"timeout":             numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectSync(ctx, args)
			},
		},
		{
			Name:        "aexp_sync_doctor",
			Description: "Check rsync availability and print recommended sync commands as JSON.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":      stringSchema("Resource name."),
				"source":        stringSchema("Optional local source."),
				"target":        stringSchema("Optional remote target."),
				"check_timeout": numberSchema("Timeout per remote check in seconds."),
				"timeout":       numberSchema("Tool timeout in seconds."),
			}, []string{"resource"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSyncDoctor(ctx, args)
			},
		},
		{
			Name:        "aexp_sync_push",
			Description: "Push local files to a resource with rsync.",
			InputSchema: objectSchema(syncSchema(map[string]interface{}{
				"resource": stringSchema("Resource name."),
				"source":   stringSchema("Local source path."),
				"target":   stringSchema("Remote target directory."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}), []string{"resource", "source", "target"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSyncPush(ctx, args)
			},
		},
		{
			Name:        "aexp_sync_pull",
			Description: "Pull files from a resource with rsync.",
			InputSchema: objectSchema(syncPullSchema(map[string]interface{}{
				"resource": stringSchema("Resource name."),
				"source":   stringSchema("Remote source path."),
				"target":   stringSchema("Local target directory."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}), []string{"resource", "source", "target"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSyncPull(ctx, args)
			},
		},
		{
			Name:        "aexp_sync_remote_pull",
			Description: "Run rsync on the remote resource so it pulls from another source.",
			InputSchema: objectSchema(syncSchema(map[string]interface{}{
				"resource": stringSchema("Resource name."),
				"source":   stringSchema("Rsync source available from the remote machine."),
				"target":   stringSchema("Remote target directory."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}), []string{"resource", "source", "target"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSyncRemotePull(ctx, args)
			},
		},
		{
			Name:        "aexp_cli",
			Description: "Run a restricted read-only aexp CLI command. Use dedicated tools for exec, submit, cancel, sync, and marks.",
			InputSchema: objectSchema(map[string]interface{}{
				"args":    arrayStringSchema("CLI arguments after aexp, e.g. [\"run\", \"list\", \"--json\"]."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"args"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolCLI(ctx, args)
			},
		},
	}
}

func resourceConnectionSchema(base map[string]interface{}) map[string]interface{} {
	base["user"] = stringSchema("SSH user.")
	base["port"] = numberSchema("SSH port.")
	base["key"] = stringSchema("SSH private key path.")
	base["auth_ref"] = stringSchema("SSH private key path alias.")
	base["socks_proxy"] = stringSchema("SOCKS5 proxy host:port.")
	base["proxy_command"] = stringSchema("SSH ProxyCommand.")
	return base
}

func resourceConfigSchema(base map[string]interface{}) map[string]interface{} {
	base["type"] = stringSchema("Resource type, usually ssh.")
	base["os_type"] = stringSchema("Operating system type, e.g. linux or macos.")
	base["port"] = numberSchema("SSH port.")
	base["user"] = stringSchema("SSH user.")
	base["auth_ref"] = stringSchema("SSH key path.")
	base["conda_base"] = stringSchema("Conda/Miniforge base prefix.")
	base["conda_init"] = stringSchema("Conda init script path.")
	base["conda_env"] = stringSchema("Default conda environment.")
	base["gpu_indices"] = stringSchema("Visible GPU indices, e.g. 0,1.")
	base["tags"] = stringSchema("Comma-separated tags.")
	base["socks_proxy"] = stringSchema("SOCKS5 proxy host:port.")
	base["proxy_command"] = stringSchema("SSH ProxyCommand.")
	return base
}

func syncSchema(base map[string]interface{}) map[string]interface{} {
	base["dry_run"] = boolSchema("Print the rsync command without running it.")
	base["delete"] = boolSchema("Delete target files that no longer exist on source.")
	base["profile"] = stringSchema("Exclude profile: code, code-data, all.")
	base["no_default_excludes"] = boolSchema("Disable profile excludes and .aexpignore.")
	base["exclude"] = arrayStringSchema("Exclude patterns.")
	base["rsync_arg"] = arrayStringSchema("Extra raw rsync arguments.")
	base["sync_timeout"] = numberSchema("Rsync timeout in seconds.")
	return base
}

func syncPullSchema(base map[string]interface{}) map[string]interface{} {
	base["dry_run"] = boolSchema("Print the rsync command without running it.")
	base["delete"] = boolSchema("Delete target files that no longer exist on source.")
	base["exclude"] = arrayStringSchema("Exclude patterns.")
	base["rsync_arg"] = arrayStringSchema("Extra raw rsync arguments.")
	base["sync_timeout"] = numberSchema("Rsync timeout in seconds.")
	return base
}

func projectInitSchema(base map[string]interface{}) map[string]interface{} {
	base["resource"] = stringSchema("Project default resource name.")
	base["cwd"] = stringSchema("Project remote working directory.")
	base["env"] = stringSchema("Project runtime env strategy: auto or raw.")
	base["conda_env"] = stringSchema("Project default conda environment.")
	base["default_gpu"] = numberSchema("Project default GPU index for formal recipes.")
	base["output"] = stringSchema("Project config output path.")
	base["force"] = boolSchema("Overwrite an existing project config.")
	base["dry_run"] = boolSchema("Print the project config without writing it.")
	base["no_events_helper"] = boolSchema("Do not create project-local aexp_events.py.")
	return base
}

func eventSchema(base map[string]interface{}) map[string]interface{} {
	base["path"] = stringSchema("Event JSONL path. Use explicit path when calling from MCP.")
	base["strict"] = boolSchema("Fail if no event path is available.")
	base["field"] = arrayStringSchema("Extra fields as key=value strings.")
	base["fields"] = mapSchema("Extra fields as an object.")
	return base
}

func objectSchema(properties map[string]interface{}, required []string) map[string]interface{} {
	s := map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func stringSchema(description string) map[string]string {
	return map[string]string{"type": "string", "description": description}
}

func numberSchema(description string) map[string]string {
	return map[string]string{"type": "number", "description": description}
}

func boolSchema(description string) map[string]string {
	return map[string]string{"type": "boolean", "description": description}
}

func arrayStringSchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "array",
		"description": description,
		"items":       map[string]string{"type": "string"},
	}
}

func mapSchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"description":          description,
		"additionalProperties": true,
	}
}
