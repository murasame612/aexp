package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
			payload := map[string]interface{}{
				"schema_version": "aexp-mcp-tool-error-v1",
				"error":          map[string]string{"message": err.Error()},
			}
			if partial := strings.TrimSpace(result); partial != "" {
				partialResult := map[string]interface{}{"stdout": partial}
				if runID := firstRunID(partial); runID != "" {
					partialResult["run_id"] = runID
					payload["run_id"] = runID
					payload["next_action"] = "Read the Run snapshot or status using the preserved run_id."
				}
				payload["partial_result"] = partialResult
			}
			return rpcResultResponse(req.ID, toolTextResult(jsonText(payload), true))
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
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	if timeoutSec > 45 {
		return "", fmt.Errorf("aexp_exec is for short bounded inspection commands and caps timeout at 45s; use aexp_submit_run for setup, training, data prep, installs, or any command that may run longer")
	}
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
	return s.runAexp(ctx, time.Duration(timeoutSec+5)*time.Second, cli...)
}

func (s *Server) toolSubmitRun(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, err := requiredString(args, "resource")
	if err != nil {
		return "", err
	}
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}

	cli := []string{"run", "submit", "--resource", resource, "--project", projectID}
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
	if v := stringArg(args, "target_env", ""); v != "" {
		cli = append(cli, "--target-env", v)
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
	for _, value := range stringSliceArg(args, "datasets") {
		cli = append(cli, "--dataset", value)
	}
	for _, value := range stringSliceArg(args, "seeds") {
		cli = append(cli, "--seed", value)
	}
	var bindingErr error
	cli, bindingErr = appendRunBindingFlags(cli, args, "inputs", "--input", "--input-json")
	if bindingErr != nil {
		return "", bindingErr
	}
	cli, bindingErr = appendRunBindingFlags(cli, args, "outputs", "--output", "--output-json")
	if bindingErr != nil {
		return "", bindingErr
	}
	if v := stringArg(args, "config_sha256", ""); v != "" {
		cli = append(cli, "--config-sha256", v)
	}
	if v := stringArg(args, "split_protocol", ""); v != "" {
		cli = append(cli, "--split-protocol", v)
	}
	if v := stringArg(args, "evaluation_protocol", ""); v != "" {
		cli = append(cli, "--evaluation-protocol", v)
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
	if v := stringArg(args, "force_reason", ""); v != "" {
		cli = append(cli, "--force-reason", v)
	}
	if v := stringArg(args, "preempt_run", ""); v != "" {
		cli = append(cli, "--preempt-run", v)
	}
	if v, ok := optionalBoolArg(args, "preempt_save"); ok && !v {
		cli = append(cli, "--preempt-save=false")
	}
	if boolArg(args, "no_gpu", false) {
		cli = append(cli, "--no-gpu")
	}
	if boolArg(args, "refresh_env", false) {
		cli = append(cli, "--refresh-env")
	}
	if boolArg(args, "allow_ephemeral_paths", false) {
		cli = append(cli, "--allow-ephemeral-paths")
	}
	if boolArg(args, "allow_dirty_git", false) {
		cli = append(cli, "--allow-dirty-git")
	}
	if boolArg(args, "record_git_diff", false) {
		cli = append(cli, "--record-git-diff")
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
			"run_id":         runID,
			"submit_output":  output,
			"status_error":   statusErr.Error(),
			"event_guidance": mcpRunEventGuidance(runID, ""),
		}), nil
	}
	return jsonText(map[string]interface{}{
		"run_id":         runID,
		"submit_output":  output,
		"status":         json.RawMessage(status),
		"event_guidance": mcpRunEventGuidance(runID, status),
	}), nil
}

func (s *Server) toolAssignRunProject(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	cli := []string{"run", "project", "set", runID, "--project", projectID, "--json"}
	if v, ok := args["expected_project_id"]; ok && v != nil {
		cli = append(cli, "--expected-project", fmt.Sprintf("%v", v))
	}
	if v := stringArg(args, "actor", ""); v != "" {
		cli = append(cli, "--actor", v)
	} else {
		cli = append(cli, "--actor", "agent")
	}
	if v := stringArg(args, "reason", ""); v != "" {
		cli = append(cli, "--reason", v)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
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
	cli := []string{"run", "list", "--json", "--summary", "--no-refresh"}
	if v := stringArg(args, "status", ""); v != "" {
		cli = append(cli, "--status", v)
	}
	if v := stringArg(args, "resource", ""); v != "" {
		cli = append(cli, "--resource", v)
	}
	if v := stringArg(args, "project", ""); v != "" {
		cli = append(cli, "--project", v)
	}
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	if v, ok := optionalIntArg(args, "cursor"); ok {
		cli = append(cli, "--offset", strconv.Itoa(v))
	}
	if boolArg(args, "trash", false) {
		cli = append(cli, "--trash")
	}
	if boolArg(args, "deleted", false) {
		cli = append(cli, "--deleted")
	}
	if boolArg(args, "important", false) {
		cli = append(cli, "--important")
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolProjectResearchContext(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	cli := []string{"project", "context", projectID, "--json"}
	for _, option := range [][2]string{
		{"map_limit", "--map-limit"}, {"thread_limit", "--thread-limit"},
		{"journal_limit", "--journal-limit"}, {"run_limit", "--run-limit"},
	} {
		if value, ok := optionalIntArg(args, option[0]); ok {
			cli = append(cli, option[1], strconv.Itoa(value))
		}
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolListWorkspacePaths(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"fs", "roots", "--json"}
	addOptionalStringFlag(&cli, args, "workspace", "--workspace")
	return s.runAexp(ctx, timeoutFromArgs(args, 10), cli...)
}

func (s *Server) toolResolvePath(ctx context.Context, args map[string]interface{}) (string, error) {
	uri, err := requiredString(args, "uri")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 10), "fs", "resolve", uri, "--json")
}

func (s *Server) toolInspectPath(ctx context.Context, args map[string]interface{}) (string, error) {
	uri, err := requiredString(args, "uri")
	if err != nil {
		return "", err
	}
	cli := []string{"fs", "stat", uri, "--json"}
	addOptionalStringFlag(&cli, args, "resource", "--on")
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolListPath(ctx context.Context, args map[string]interface{}) (string, error) {
	uri, err := requiredString(args, "uri")
	if err != nil {
		return "", err
	}
	cli := []string{"fs", "ls", uri, "--json", "--limit", strconv.Itoa(intArg(args, "limit", 100))}
	addOptionalStringFlag(&cli, args, "resource", "--on")
	addOptionalStringFlag(&cli, args, "cursor", "--cursor")
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolStorageStat(ctx context.Context, args map[string]interface{}) (string, error) {
	uri, err := requiredString(args, "uri")
	if err != nil {
		return "", err
	}
	cli := []string{"storage", "stat", uri, "--json"}
	addOptionalStringFlag(&cli, args, "resource", "--on")
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolStorageList(ctx context.Context, args map[string]interface{}) (string, error) {
	uri, err := requiredString(args, "uri")
	if err != nil {
		return "", err
	}
	cli := []string{"storage", "ls", uri, "--json", "--limit", strconv.Itoa(intArg(args, "limit", 50))}
	addOptionalStringFlag(&cli, args, "resource", "--on")
	addOptionalStringFlag(&cli, args, "cursor", "--cursor")
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolStorageLocations(ctx context.Context, args map[string]interface{}) (string, error) {
	uri, err := requiredString(args, "uri")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), "storage", "locations", uri, "--json")
}

func (s *Server) toolStorageCopy(ctx context.Context, args map[string]interface{}) (string, error) {
	source, err := requiredString(args, "source")
	if err != nil {
		return "", err
	}
	destination, err := requiredString(args, "destination")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 120), "storage", "copy", source, destination, "--json")
}

func transferPlanArgs(args map[string]interface{}, action string) ([]string, error) {
	source, err := requiredString(args, "source")
	if err != nil {
		return nil, err
	}
	destination, err := requiredString(args, "destination")
	if err != nil {
		return nil, err
	}
	cli := []string{"transfer", action, source, destination, "--json"}
	if value := stringArg(args, "source_revision", ""); value != "" {
		cli = append(cli, "--source-revision", value)
	}
	if value := stringArg(args, "initiator", ""); value != "" {
		cli = append(cli, "--initiator", value)
	}
	if value := stringArg(args, "verification", ""); value != "" {
		cli = append(cli, "--verify", value)
	}
	return cli, nil
}

func (s *Server) toolPlanTransfer(ctx context.Context, args map[string]interface{}) (string, error) {
	cli, err := transferPlanArgs(args, "plan")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolStartTransfer(ctx context.Context, args map[string]interface{}) (string, error) {
	cli, err := transferPlanArgs(args, "start")
	if err != nil {
		return "", err
	}
	expected, err := requiredString(args, "expected_plan_sha256")
	if err != nil {
		return "", err
	}
	cli = append(cli, "--plan-sha256", expected)
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEnsurePath(ctx context.Context, args map[string]interface{}) (string, error) {
	cli, err := transferPlanArgs(args, "start")
	if err != nil {
		return "", err
	}
	// The argument and flag surface is deliberately identical to transfer
	// start, while the CLI name keeps the Agent's operation path-oriented.
	cli[0], cli[1] = "fs", "ensure"
	expected, err := requiredString(args, "expected_plan_sha256")
	if err != nil {
		return "", err
	}
	cli = append(cli, "--plan-sha256", expected)
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolGetTransfer(ctx context.Context, args map[string]interface{}) (string, error) {
	id, err := requiredString(args, "transfer_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 10), "transfer", "status", id, "--json")
}

func (s *Server) toolTransferMutation(ctx context.Context, args map[string]interface{}, action string) (string, error) {
	id, err := requiredString(args, "transfer_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 10), "transfer", action, id, "--json")
}

func (s *Server) toolRunLifecycle(ctx context.Context, args map[string]interface{}, action string) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "run", action, runID)
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

func (s *Server) toolRunSnapshot(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	last := intArg(args, "last", intArg(args, "last_n", 500))
	cli := []string{"run", "snapshot", runID, "--json", "--tail", strconv.Itoa(last)}
	if boolArg(args, "refresh", false) {
		cli = append(cli, "--refresh")
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolTailRunEvents(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	last := intArg(args, "last", intArg(args, "last_n", 50))
	cli := []string{"run", "events", runID, "--json", "--tail", strconv.Itoa(last)}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolRunMetrics(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	last := intArg(args, "last", intArg(args, "last_n", 500))
	cli := []string{"run", "metrics", runID, "--json", "--latest", "--tail", strconv.Itoa(last)}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolRunEventQuality(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	last := intArg(args, "last", intArg(args, "last_n", 100000))
	maxIssues := intArg(args, "max_issues", 200)
	cli := []string{"run", "event-quality", runID, "--json", "--tail", strconv.Itoa(last), "--max-issues", strconv.Itoa(maxIssues)}
	return s.runAexp(ctx, timeoutFromArgs(args, 30), cli...)
}

func (s *Server) toolCLI(ctx context.Context, args map[string]interface{}) (string, error) {
	cliArgs := stringSliceArg(args, "args")
	if len(cliArgs) == 0 {
		return "", errors.New("args is required")
	}
	if err := validateCLIArgs(cliArgs); err != nil {
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

func (s *Server) toolProjectCard(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	cli := []string{"project", "card", runID, "--json"}
	addOptionalStringFlag(&cli, args, "config", "--config")
	addOptionalStringFlag(&cli, args, "question", "--question")
	addOptionalStringFlag(&cli, args, "verdict", "--verdict")
	addOptionalStringFlag(&cli, args, "level", "--level")
	for _, metric := range stringSliceArg(args, "metric") {
		cli = append(cli, "--metric", metric)
	}
	for _, artifact := range stringSliceArg(args, "artifact") {
		cli = append(cli, "--artifact", artifact)
	}
	addOptionalStringFlag(&cli, args, "supports", "--supports")
	addOptionalStringFlag(&cli, args, "weakens", "--weakens")
	addOptionalStringFlag(&cli, args, "next_action", "--next-action")
	addBoolFlag(&cli, args, "important", "--important")
	addBoolFlag(&cli, args, "promote", "--promote")
	addBoolFlag(&cli, args, "reassign_project", "--reassign-project")
	addOptionalStringFlag(&cli, args, "proposal_reason", "--proposal-reason")
	for _, run := range stringSliceArg(args, "related_run") {
		cli = append(cli, "--related-run", run)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolProjectRuns(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"project", "runs", "--json"}
	addOptionalStringFlag(&cli, args, "config", "--config")
	addBoolFlag(&cli, args, "important", "--important")
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolProjectDigest(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"project", "digest"}
	addOptionalStringFlag(&cli, args, "config", "--config")
	addBoolFlag(&cli, args, "important", "--important")
	addBoolFlag(&cli, args, "json", "--json")
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceList(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"evidence", "list", "--json"}
	addOptionalStringFlag(&cli, args, "query", "--query")
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolProjectEvidenceGraphs(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	limit := intArg(args, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	status := stringArg(args, "status", "active")
	cli := []string{"evidence", "list", "--project", projectID, "--status", status, "--limit", strconv.Itoa(limit), "--json"}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	title, err := requiredString(args, "title")
	if err != nil {
		return "", err
	}
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "create", title, "--json"}
	cli = append(cli, "--project", projectID)
	addOptionalStringFlag(&cli, args, "description", "--description")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolCreateTopicEvidenceGraph(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	title, err := requiredString(args, "title")
	if err != nil {
		return "", err
	}
	purpose, err := requiredString(args, "purpose")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "create", title, "--project", projectID, "--purpose", purpose, "--json"}
	for _, recipe := range stringSliceArg(args, "recipes") {
		cli = append(cli, "--recipe", recipe)
	}
	for _, keyword := range stringSliceArg(args, "keywords") {
		cli = append(cli, "--keyword", keyword)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceGet(ctx context.Context, args map[string]interface{}) (string, error) {
	chainID, err := requiredString(args, "chain_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "show", chainID, "--json")
}

func (s *Server) toolEvidenceThreadMap(ctx context.Context, args map[string]interface{}) (string, error) {
	mapID, err := requiredString(args, "map_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "threads", mapID, "--json")
}

func (s *Server) toolEvidenceAudit(ctx context.Context, args map[string]interface{}) (string, error) {
	mapID, err := requiredString(args, "map_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "audit", mapID, "--json")
}

func (s *Server) toolArchiveEvidenceMap(ctx context.Context, args map[string]interface{}) (string, error) {
	mapID, err := requiredString(args, "map_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "archive", mapID, "--json")
}

func (s *Server) toolEvidencePropose(ctx context.Context, args map[string]interface{}) (string, error) {
	runID, err := requiredString(args, "run_id")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "propose", runID, "--json"}
	addOptionalStringFlag(&cli, args, "chain_id", "--chain")
	addOptionalStringFlag(&cli, args, "patch_json", "--patch-json")
	if v, ok := optionalIntArg(args, "base_revision"); ok {
		cli = append(cli, "--base-revision", strconv.Itoa(v))
	}
	addBoolFlag(&cli, args, "no_graph_impact", "--no-graph-impact")
	addOptionalStringFlag(&cli, args, "reason", "--reason")
	addOptionalStringFlag(&cli, args, "routing_reason", "--routing-reason")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolProposeEvidenceGraph(ctx context.Context, args map[string]interface{}) (string, error) {
	forwarded := make(map[string]interface{}, len(args)+1)
	for key, value := range args {
		forwarded[key] = value
	}
	if graphID := stringArg(args, "graph_id", ""); graphID != "" {
		forwarded["chain_id"] = graphID
	}
	return s.toolEvidencePropose(ctx, forwarded)
}

func (s *Server) toolEvidenceProposalPlan(ctx context.Context, args map[string]interface{}) (string, error) {
	proposalID := stringArg(args, "proposal_id", "")
	runID := stringArg(args, "run_id", "")
	if proposalID == "" && runID == "" {
		return "", fmt.Errorf("proposal_id is required (run_id is accepted only for legacy Run Card proposals)")
	}
	target := proposalID
	if target == "" {
		target = runID
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "proposal-plan", target, "--json")
}

func (s *Server) toolEvidenceReorganizationPlan(ctx context.Context, args map[string]interface{}) (string, error) {
	mapID, err := requiredString(args, "map_id")
	if err != nil {
		return "", err
	}
	patchJSON, err := requiredString(args, "patch_json")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "reorganization-plan", mapID, "--patch-json", patchJSON, "--json")
}

func (s *Server) toolEvidenceReorganizationCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	mapID, err := requiredString(args, "map_id")
	if err != nil {
		return "", err
	}
	summary, err := requiredString(args, "summary")
	if err != nil {
		return "", err
	}
	patchJSON, err := requiredString(args, "patch_json")
	if err != nil {
		return "", err
	}
	expectedPlanHash, err := requiredString(args, "expected_plan_hash")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "reorganization-create", mapID, "--summary", summary, "--patch-json", patchJSON, "--expected-plan-hash", expectedPlanHash, "--json"}
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	addOptionalStringFlag(&cli, args, "routing_reason", "--routing-reason")
	for _, runID := range stringSliceArg(args, "source_run_ids") {
		cli = append(cli, "--source-run", runID)
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceProposalRebase(ctx context.Context, args map[string]interface{}) (string, error) {
	proposalID, err := requiredString(args, "proposal_id")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "proposal-rebase", proposalID, "--json"}
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceProposalReview(ctx context.Context, args map[string]interface{}) (string, error) {
	proposalID := stringArg(args, "proposal_id", "")
	runID := stringArg(args, "run_id", "")
	if proposalID == "" && runID == "" {
		return "", fmt.Errorf("proposal_id is required (run_id is accepted only for legacy Run Card proposals)")
	}
	target := proposalID
	if target == "" {
		target = runID
	}
	action, err := requiredString(args, "action")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "proposal-review", target, "--json", "--action", action}
	addOptionalStringFlag(&cli, args, "reviewer", "--reviewer")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceProposalSubmit(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	summary, err := requiredString(args, "summary")
	if err != nil {
		return "", err
	}
	patchJSON, err := requiredString(args, "patch_json")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "proposal-submit", projectID, "--json", "--summary", summary, "--patch-json", patchJSON}
	addOptionalStringFlag(&cli, args, "target_map_id", "--target-map")
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	addOptionalStringFlag(&cli, args, "routing_reason", "--routing-reason")
	for _, runID := range stringSliceArg(args, "source_run_ids") {
		cli = append(cli, "--source-run", runID)
	}
	for _, snapshotID := range stringSliceArg(args, "source_snapshot_ids") {
		cli = append(cli, "--source-snapshot", snapshotID)
	}
	addBoolFlag(&cli, args, "project_level_impact", "--project-level-impact")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceBranchFromOutcome(ctx context.Context, args map[string]interface{}) (string, error) {
	mapID, err := requiredString(args, "map_id")
	if err != nil {
		return "", err
	}
	outcomeNodeID, err := requiredString(args, "outcome_node_id")
	if err != nil {
		return "", err
	}
	hypothesisTitle, err := requiredString(args, "hypothesis_title")
	if err != nil {
		return "", err
	}
	branchRationale, err := requiredString(args, "branch_rationale")
	if err != nil {
		return "", err
	}
	cli := []string{
		"evidence", "branch-from-outcome", mapID, "--json",
		"--outcome-node", outcomeNodeID,
		"--hypothesis-title", hypothesisTitle,
		"--branch-rationale", branchRationale,
	}
	addOptionalStringFlag(&cli, args, "hypothesis_body_md", "--hypothesis-body-md")
	addOptionalStringFlag(&cli, args, "experiment_design_title", "--experiment-design-title")
	addOptionalStringFlag(&cli, args, "experiment_design_body_md", "--experiment-design-body-md")
	addOptionalStringFlag(&cli, args, "summary", "--summary")
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceProposalList(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "proposal-list", projectID, "--json"}
	addOptionalStringFlag(&cli, args, "status", "--status")
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidenceProposalGet(ctx context.Context, args map[string]interface{}) (string, error) {
	proposalID, err := requiredString(args, "proposal_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "evidence", "proposal-get", proposalID, "--json")
}

func (s *Server) toolEvidenceProposalReroute(ctx context.Context, args map[string]interface{}) (string, error) {
	proposalID, err := requiredString(args, "proposal_id")
	if err != nil {
		return "", err
	}
	targetMapID, err := requiredString(args, "target_map_id")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "proposal-reroute", proposalID, "--target-map", targetMapID, "--json"}
	addOptionalStringFlag(&cli, args, "routing_reason", "--routing-reason")
	addBoolFlag(&cli, args, "project_level_impact", "--project-level-impact")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidencePromotionPlan(ctx context.Context, args map[string]interface{}) (string, error) {
	sourceMapID, err := requiredString(args, "source_map_id")
	if err != nil {
		return "", err
	}
	summary, err := requiredString(args, "summary")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "promotion-plan", sourceMapID, "--summary", summary, "--json"}
	for _, nodeID := range stringSliceArg(args, "source_node_ids") {
		cli = append(cli, "--source-node", nodeID)
	}
	addOptionalStringFlag(&cli, args, "node_type", "--node-type")
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolEvidencePromotionCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	sourceMapID, err := requiredString(args, "source_map_id")
	if err != nil {
		return "", err
	}
	summary, err := requiredString(args, "summary")
	if err != nil {
		return "", err
	}
	expectedPlanHash, err := requiredString(args, "expected_plan_hash")
	if err != nil {
		return "", err
	}
	cli := []string{"evidence", "promotion-create", sourceMapID, "--summary", summary, "--expected-plan-hash", expectedPlanHash, "--json"}
	for _, nodeID := range stringSliceArg(args, "source_node_ids") {
		cli = append(cli, "--source-node", nodeID)
	}
	addOptionalStringFlag(&cli, args, "node_type", "--node-type")
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolMatrixList(ctx context.Context, args map[string]interface{}) (string, error) {
	cli := []string{"matrix", "list", "--json"}
	addOptionalStringFlag(&cli, args, "query", "--query")
	if v, ok := optionalIntArg(args, "limit"); ok {
		cli = append(cli, "--limit", strconv.Itoa(v))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolMatrixCreate(ctx context.Context, args map[string]interface{}) (string, error) {
	title, err := requiredString(args, "title")
	if err != nil {
		return "", err
	}
	cli := []string{"matrix", "create", title, "--json"}
	addOptionalStringFlag(&cli, args, "description", "--description")
	for _, column := range stringSliceArg(args, "columns") {
		cli = append(cli, "--column", column)
	}
	if boolArg(args, "no_defaults", false) {
		cli = append(cli, "--no-defaults")
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolMatrixGet(ctx context.Context, args map[string]interface{}) (string, error) {
	matrixID, err := requiredString(args, "matrix_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "matrix", "show", matrixID, "--json")
}

func (s *Server) toolMatrixSetCell(ctx context.Context, args map[string]interface{}) (string, error) {
	matrixID, err := requiredString(args, "matrix_id")
	if err != nil {
		return "", err
	}
	row, err := requiredString(args, "row")
	if err != nil {
		return "", err
	}
	column, err := requiredString(args, "column")
	if err != nil {
		return "", err
	}
	cli := []string{"matrix", "set", matrixID, "--json", "--row", row, "--column", column}
	addOptionalStringFlag(&cli, args, "value", "--value")
	addOptionalStringFlag(&cli, args, "run_id", "--run-id")
	addOptionalStringFlag(&cli, args, "project_card_id", "--project-card-id")
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
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
	if v := stringArg(args, "statement", ""); v != "" {
		cli = append(cli, "--statement", v)
	}
	if v := stringArg(args, "body_md", ""); v != "" {
		cli = append(cli, "--body-md", v)
	}
	if v := stringArg(args, "reason", ""); v != "" {
		cli = append(cli, "--reason", v)
	}
	if v := stringArg(args, "evidence", ""); v != "" {
		cli = append(cli, "--evidence", v)
	}
	for _, attachment := range stringSliceArg(args, "attachment") {
		cli = append(cli, "--attach", attachment)
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

func (s *Server) toolCreateProjectJournalEntry(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	title, err := requiredString(args, "title")
	if err != nil {
		return "", err
	}
	cli := []string{"project", "journal", "create", projectID, "--title", title, "--json"}
	addOptionalStringFlag(&cli, args, "actor", "--actor")
	addOptionalStringFlag(&cli, args, "body_md", "--body-md")
	addOptionalStringFlag(&cli, args, "next_action", "--next-action")
	for _, runID := range stringSliceArg(args, "run_ids") {
		cli = append(cli, "--run", runID)
	}
	if refs, ok := args["literature_refs"]; ok && refs != nil {
		encoded, err := json.Marshal(refs)
		if err != nil {
			return "", fmt.Errorf("encode literature_refs: %w", err)
		}
		cli = append(cli, "--literature-refs-json", string(encoded))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolListProjectJournal(ctx context.Context, args map[string]interface{}) (string, error) {
	projectID, err := requiredString(args, "project_id")
	if err != nil {
		return "", err
	}
	cli := []string{"project", "journal", "list", projectID, "--json"}
	addOptionalStringFlag(&cli, args, "run_id", "--run")
	addOptionalStringFlag(&cli, args, "query", "--query")
	addOptionalStringFlag(&cli, args, "next_action_status", "--next-action-status")
	if limit := intArg(args, "limit", 0); limit > 0 {
		cli = append(cli, "--limit", strconv.Itoa(limit))
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), cli...)
}

func (s *Server) toolGetProjectJournalEntry(ctx context.Context, args map[string]interface{}) (string, error) {
	entryID, err := requiredString(args, "entry_id")
	if err != nil {
		return "", err
	}
	return s.runAexp(ctx, timeoutFromArgs(args, 20), "project", "journal", "show", entryID, "--json")
}

func (s *Server) toolUpdateProjectJournalNextAction(ctx context.Context, args map[string]interface{}) (string, error) {
	entryID, err := requiredString(args, "entry_id")
	if err != nil {
		return "", err
	}
	status, err := requiredString(args, "status")
	if err != nil {
		return "", err
	}
	return s.runAexp(
		ctx,
		timeoutFromArgs(args, 20),
		"project", "journal", "next-action", entryID, "--status", status, "--json",
	)
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
	addOptionalStringFlag(&cli, args, "target_env", "--target-env")
	addOptionalStringFlag(&cli, args, "ui_events", "--ui-events")
	if v, ok := optionalIntArg(args, "gpu_index"); ok {
		cli = append(cli, "--gpu-index", strconv.Itoa(v))
	}
	addBoolFlag(&cli, args, "no_gpu", "--no-gpu")
	addBoolFlag(&cli, args, "force", "--force")
	addOptionalStringFlag(&cli, args, "force_reason", "--force-reason")
	addOptionalStringFlag(&cli, args, "preempt_run", "--preempt-run")
	if v, ok := optionalBoolArg(args, "preempt_save"); ok && !v {
		cli = append(cli, "--preempt-save=false")
	}
	addBoolFlag(&cli, args, "dry_run", "--dry-run")
	addBoolFlag(&cli, args, "refresh_env", "--refresh-env")
	addBoolFlag(&cli, args, "allow_ephemeral_paths", "--allow-ephemeral-paths")
	addBoolFlag(&cli, args, "allow_dirty_git", "--allow-dirty-git")
	addBoolFlag(&cli, args, "record_git_diff", "--record-git-diff")
	if v, ok := optionalIntArg(args, "launch_timeout"); ok {
		cli = append(cli, "--launch-timeout", strconv.Itoa(v))
	}
	output, err := s.runAexp(ctx, timeoutFromArgs(args, 90), cli...)
	if err != nil {
		return output, err
	}
	if boolArg(args, "dry_run", false) {
		return output, nil
	}
	runID := firstRunID(output)
	if runID == "" {
		return output, nil
	}
	status, statusErr := s.runAexp(ctx, 20*time.Second, "run", "status", runID, "--short", "--json")
	if statusErr != nil {
		return jsonText(map[string]interface{}{
			"run_id":         runID,
			"submit_output":  output,
			"status_error":   statusErr.Error(),
			"event_guidance": mcpRunEventGuidance(runID, ""),
		}), nil
	}
	return jsonText(map[string]interface{}{
		"run_id":         runID,
		"submit_output":  output,
		"status":         json.RawMessage(status),
		"event_guidance": mcpRunEventGuidance(runID, status),
	}), nil
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

func (s *Server) toolSyncDatasetPush(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, source, target, err := syncEndpointArgs(args, "source", "target")
	if err != nil {
		return "", err
	}
	cli := []string{"sync", "dataset", "push", "--resource", resource}
	addBoolFlag(&cli, args, "dry_run", "--dry-run")
	addBoolFlag(&cli, args, "delete", "--delete")
	if v, ok := optionalBoolArg(args, "verify"); ok && !v {
		cli = append(cli, "--no-verify")
	}
	for _, p := range stringSliceArg(args, "exclude") {
		cli = append(cli, "--exclude", p)
	}
	if v, ok := optionalIntArg(args, "sync_timeout"); ok {
		cli = append(cli, "--timeout", strconv.Itoa(v))
	}
	if v, ok := optionalIntArg(args, "retries"); ok {
		cli = append(cli, "--retries", strconv.Itoa(v))
	}
	cli = append(cli, source, target)
	return s.runAexp(ctx, timeoutFromArgs(args, 300), cli...)
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
	addOptionalStringFlag(cli, args, "remote_path", "--remote-path")
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
	addOptionalStringFlag(cli, args, "series", "--series")
	addOptionalStringFlag(cli, args, "run", "--run")
	addOptionalStringFlag(cli, args, "variant", "--variant")
	addOptionalStringFlag(cli, args, "split", "--split")
	addOptionalStringFlag(cli, args, "stage", "--stage")
	addOptionalStringFlag(cli, args, "label", "--label")
	addOptionalStringFlag(cli, args, "trial", "--trial")
	addOptionalStringFlag(cli, args, "seed", "--seed")
	addOptionalStringFlag(cli, args, "fold", "--fold")
	for _, field := range stringSliceArg(args, "field") {
		*cli = append(*cli, "--field", field)
	}
	for k, v := range stringMapArg(args, "fields") {
		*cli = append(*cli, "--field", k+"="+v)
	}
}

func validateCLIArgs(args []string) error {
	if len(args) == 0 {
		return errors.New("args is required")
	}
	if strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("first arg must be a command, got %q", args[0])
	}
	switch args[0] {
	case "mcp", "serve":
		return fmt.Errorf("aexp_cli does not start long-lived %q; run it outside MCP", args[0])
	case "event", "events":
		return fmt.Errorf("aexp_cli does not emit manual training events; instrument the training/eval script with aexp_events.py before submitting the run, and use aexp_create_project_journal_entry for post-hoc reasoning")
	case "evidence":
		if len(args) > 1 && (args[1] == "add-node" || args[1] == "add-edge" || args[1] == "save") {
			return fmt.Errorf("aexp_cli does not mutate accepted Evidence directly; read the Research Thread projection and submit a reviewed Evidence proposal")
		}
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

func mcpRunEventGuidance(runID, statusJSON string) map[string]interface{} {
	uiEvents := ".aexp/events/" + runID + ".jsonl"
	if statusJSON != "" {
		var status map[string]interface{}
		if err := json.Unmarshal([]byte(statusJSON), &status); err == nil {
			if v, ok := status["ui_events"].(string); ok && strings.TrimSpace(v) != "" {
				uiEvents = v
			}
		}
	}
	return map[string]interface{}{
		"ui_events": uiEvents,
		"env": map[string]string{
			"AEXP_UI_EVENTS": uiEvents,
		},
		"purpose": "Training telemetry must be produced by the training/eval code while the run is executing. Do not reconstruct loss, metric, progress, or parameter events after the run.",
		"python": []string{
			"from aexp_events import metric, training_epoch, training_done, progress, param, note",
			"param(\"model\", model_name, trial=trial_id, variant=variant)",
			"metric(\"train/loss\", loss, epoch=local_epoch, trial=trial_id, variant=variant, stage=\"train\")",
			"metric(\"val/loss\", val_loss, epoch=local_epoch, trial=trial_id, variant=variant, split=\"val\", stage=\"eval\")",
			"training_epoch(local_epoch, total=max_epochs, trial=trial_id, variant=variant)",
			"training_done(epoch=last_epoch, total=max_epochs, best_epoch=best_epoch, early_stopped=early_stopped, trial=trial_id, variant=variant)",
		},
		"rules": []string{
			"Add aexp_events instrumentation to the training/eval script before submitting the run; do not use manual event tools for post-hoc telemetry.",
			"Keep metric/progress names short and stable, e.g. train/loss, val/loss, val/mse, epoch, trial.",
			"Put model, dataset, split, stage, seed, fold, and hyperparameter-trial context in series/run/variant/split/stage/trial fields.",
			"Use training_epoch/training_done for training-loop progress; on early stop, keep the actual last epoch and set early_stopped=true instead of forcing epoch to total.",
			"For sweeps, epoch is the local epoch inside that trial; use trial/variant/series for global sweep identity so loss curves do not start halfway across the chart.",
			"Do not embed a full experiment config or trial id in the metric name; the UI uses context fields to draw one chart with multiple series.",
			"After the run, use aexp_create_project_journal_entry for interpretation and next steps; Project journal entries are reasoning, not training telemetry.",
			"Snapshot/events readers cache successful UI event reads locally, so later offline resource or temporary-container loss can still show the last known event stream.",
		},
		"monitor": []string{
			"aexp_get_run_snapshot(run_id=\"" + runID + "\")",
			"aexp_tail_run_events(run_id=\"" + runID + "\", last=50)",
			"aexp_get_run_metrics(run_id=\"" + runID + "\")",
		},
		"polling": "Prefer snapshot/events/metrics. Poll every 30-60s, then back off toward 120s when progress has not changed. Successful event reads refresh the local event cache; use raw logs only for failures or missing events.",
	}
}

func mcpStatusMonitoringHint(runID string) map[string]interface{} {
	return map[string]interface{}{
		"purpose":              "diagnostic_or_final_check",
		"preferred_tool":       "aexp_get_run_snapshot",
		"preferred_call":       "aexp_get_run_snapshot(run_id=\"" + runID + "\")",
		"events_tool":          "aexp_tail_run_events",
		"metrics_tool":         "aexp_get_run_metrics",
		"suggested_interval":   "Poll snapshot every 30-60s, then back off toward 120s when progress has not changed.",
		"avoid_for_monitoring": true,
	}
}

func addStatusMonitoringHint(runID string, statusJSON string) string {
	var status map[string]interface{}
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		return jsonText(map[string]interface{}{
			"run_id":     runID,
			"raw_status": statusJSON,
			"monitoring": mcpStatusMonitoringHint(runID),
		})
	}
	status["monitoring"] = mcpStatusMonitoringHint(runID)
	return jsonText(status)
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

func optionalBoolArg(args map[string]interface{}, key string) (bool, bool) {
	if args == nil {
		return false, false
	}
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
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

func appendRunBindingFlags(cli []string, args map[string]interface{}, key, legacyFlag, jsonFlag string) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return cli, nil
	}
	items, ok := value.([]interface{})
	if !ok {
		if strings, stringOK := value.([]string); stringOK {
			for _, item := range strings {
				cli = append(cli, legacyFlag, item)
			}
			return cli, nil
		}
		return nil, fmt.Errorf("%s must be an array", key)
	}
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				cli = append(cli, legacyFlag, strings.TrimSpace(typed))
			}
		case map[string]interface{}:
			raw, err := json.Marshal(typed)
			if err != nil {
				return nil, fmt.Errorf("encode %s binding: %w", key, err)
			}
			cli = append(cli, jsonFlag, string(raw))
		default:
			return nil, fmt.Errorf("%s items must be objects or legacy strings", key)
		}
	}
	return cli, nil
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
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("AEXP_MCP_TOOL_PROFILE")))
	if profile == "" {
		profile = "research"
	}
	for _, tool := range tools {
		if !toolVisibleInProfile(tool.Name, profile) {
			continue
		}
		out = append(out, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
	}
	return out
}

func toolVisibleInProfile(name, profile string) bool {
	switch profile {
	case "research":
		return defaultResearchTool(name)
	case "advanced":
		return defaultResearchTool(name) || advancedResearchTool(name)
	case "admin":
		return defaultResearchTool(name) || administrativeTool(name)
	case "all", "compatibility":
		return true
	default:
		return defaultResearchTool(name)
	}
}

func defaultResearchTool(name string) bool {
	switch name {
	case "aexp_agent_card",
		"aexp_project_list", "aexp_get_project_research_context",
		"aexp_literature_status", "aexp_literature_query",
		"aexp_create_project_journal_entry", "aexp_get_project_journal_entry",
		"aexp_list_resources", "aexp_submit_run", "aexp_list_runs", "aexp_get_run_snapshot", "aexp_tail_run_logs",
		"aexp_create_evidence_proposal", "aexp_plan_evidence_graph_proposal":
		return true
	default:
		return false
	}
}

func advancedResearchTool(name string) bool {
	switch name {
	case "aexp_project_get", "aexp_list_project_journal", "aexp_update_project_journal_next_action",
		"aexp_asset_publish", "aexp_asset_get", "aexp_asset_list", "aexp_assign_run_project",
		"aexp_get_run_status", "aexp_cancel_run", "aexp_create_evidence_snapshot", "aexp_get_evidence_snapshot",
		"aexp_list_evidence_snapshots", "aexp_evaluate_evidence_release", "aexp_list_evidence_releases",
		"aexp_list_project_evidence_graphs", "aexp_create_topic_evidence_graph", "aexp_get_evidence_chain",
		"aexp_get_evidence_thread_map", "aexp_audit_evidence_map", "aexp_archive_evidence_map",
		"aexp_branch_from_outcome", "aexp_list_evidence_proposals", "aexp_get_evidence_proposal",
		"aexp_reroute_evidence_proposal", "aexp_plan_evidence_promotion", "aexp_create_evidence_promotion",
		"aexp_plan_evidence_reorganization", "aexp_create_evidence_reorganization_proposal",
		"aexp_rebase_evidence_proposal":
		return true
	default:
		return false
	}
}

func administrativeTool(name string) bool {
	switch name {
	case "aexp_project_init", "aexp_project_detect", "aexp_project_doctor", "aexp_doctor",
		"aexp_archive_evidence_map", "aexp_assign_run_project", "aexp_asset_publish",
		"aexp_asset_get", "aexp_asset_list", "aexp_cancel_run":
		return true
	default:
		return false
	}
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
			Name:        "aexp_asset_publish",
			Description: "Publish and fully verify an immutable Asset revision. The managed transfer and NAS placement are selected by aexp.",
			InputSchema: objectSchema(map[string]interface{}{
				"asset":   stringSchema("Asset identity as name@revision."),
				"from":    stringSchema("Local source file or directory."),
				"to":      stringSchema("Destination aexp:// logical path."),
				"dry_run": boolSchema("Return the side-effect-free publish plan."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"asset", "from", "to"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				asset, err := requiredString(args, "asset")
				if err != nil {
					return "", err
				}
				source, err := requiredString(args, "from")
				if err != nil {
					return "", err
				}
				destination, err := requiredString(args, "to")
				if err != nil {
					return "", err
				}
				cliArgs := []string{"asset", "publish", asset, "--from", source, "--to", destination, "--json"}
				if boolArg(args, "dry_run", false) {
					cliArgs = append(cliArgs, "--dry-run")
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 300), cliArgs...)
			},
		},
		{
			Name:        "aexp_project_list",
			Description: "List canonical Projects. Projects scope Assets, Runs, one primary evidence graph, and optional topic graphs.",
			InputSchema: objectSchema(map[string]interface{}{
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.runAexp(ctx, timeoutFromArgs(args, 30), "project", "list", "--json")
			},
		},
		{
			Name:        "aexp_get_project_research_context",
			Description: "Default Agent entry for one Project. Returns a compact project-research-context-v2 summary of Topic routing, Research Threads, the Project literature binding, recent Journal decisions, paged Run discovery hints, pending Evidence proposals, warnings, and explicit next_reads. The literature binding is a project-first frozen-corpus default, not a boundary on read-only Zotero discovery. It deliberately omits full graph bodies, Journal Markdown, logs, artifact manifests, and Run provenance; use aexp_list_runs for paged history and follow next_reads only when relevant.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":    stringSchema("Canonical Project id."),
				"map_limit":     numberSchema("Maximum active Maps summarized; default 8, maximum 20."),
				"thread_limit":  numberSchema("Maximum Research Threads summarized per Map; default 3, maximum 8."),
				"journal_limit": numberSchema("Maximum recent Journal entries summarized; default 5, maximum 20."),
				"run_limit":     numberSchema("Maximum recent compact Run summaries; default 8, maximum 30. Use aexp_list_runs for the full paged history."),
				"timeout":       numberSchema("Tool timeout in seconds."),
			}, []string{"project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectResearchContext(ctx, args)
			},
		},
		{
			Name:        "aexp_literature_catalog",
			Description: "List human-readable local Zotero collections and the frozen PaperQA2 profiles currently ready for reproducible Project queries. Use this before binding; live discovery remains available through the existing Zotero MCP.",
			InputSchema: objectSchema(map[string]interface{}{
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.runAexp(ctx, timeoutFromArgs(args, 25), "literature", "catalog", "--json")
			},
		},
		{
			Name:        "aexp_bind_project_literature",
			Description: "Bind a Project to one ready frozen-corpus profile after choosing it from aexp_literature_catalog. This changes only Project literature defaults; it does not constrain read-only Zotero discovery or alter Run provenance.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":            stringSchema("Canonical Project id."),
				"zotero_collection_key": stringSchema("Collection key returned by aexp_literature_catalog."),
				"service_profile":       stringSchema("Ready profile name returned by aexp_literature_catalog."),
				"timeout":               numberSchema("Tool timeout in seconds."),
			}, []string{"project_id", "zotero_collection_key", "service_profile"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				projectID, err := requiredString(args, "project_id")
				if err != nil {
					return "", err
				}
				collectionKey, err := requiredString(args, "zotero_collection_key")
				if err != nil {
					return "", err
				}
				profile, err := requiredString(args, "service_profile")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 30), "literature", "bind", projectID, "--zotero-collection", collectionKey, "--service-profile", profile, "--json")
			},
		},
		{
			Name:        "aexp_literature_status",
			Description: "Check the Project's Zotero collection binding and active PaperQA2 corpus revision. This is read-only literature background and cannot satisfy experiment provenance.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id": stringSchema("Canonical Project id."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				projectID, err := requiredString(args, "project_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 25), "literature", "status", projectID, "--json")
			},
		},
		{
			Name:        "aexp_literature_query",
			Description: "Query the Project-bound PaperQA2 frozen corpus and return revision-bound Zotero citations. This is the reproducible project-first path, not a restriction on read-only discovery: use the existing Zotero MCP to search the live library or close-read papers when needed. Results are literature background only and must never be presented as Run facts or accepted experimental Evidence.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":         stringSchema("Canonical Project id."),
				"query":              stringSchema("Literature question."),
				"evidence_k":         numberSchema("Candidate evidence count; default 10."),
				"answer_max_sources": numberSchema("Maximum cited sources; default 6."),
				"timeout":            numberSchema("Tool timeout in seconds."),
			}, []string{"project_id", "query"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				projectID, err := requiredString(args, "project_id")
				if err != nil {
					return "", err
				}
				query, err := requiredString(args, "query")
				if err != nil {
					return "", err
				}
				cli := []string{"literature", "query", projectID, "--query", query, "--json"}
				if value, ok := optionalIntArg(args, "evidence_k"); ok {
					cli = append(cli, "--evidence-k", strconv.Itoa(value))
				}
				if value, ok := optionalIntArg(args, "answer_max_sources"); ok {
					cli = append(cli, "--answer-max-sources", strconv.Itoa(value))
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 300), cli...)
			},
		},
		{
			Name:        "aexp_project_get",
			Description: "Inspect one canonical Project and resolve its active primary Evidence Map without asking the Agent to choose a map.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id": stringSchema("Canonical Project id."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				projectID, err := requiredString(args, "project_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 30), "project", "get", projectID, "--json")
			},
		},
		{
			Name:        "aexp_asset_get",
			Description: "Get one immutable Asset revision, including its verified manifest identity and storage URI.",
			InputSchema: objectSchema(map[string]interface{}{
				"asset":   stringSchema("Asset identity as name@revision."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"asset"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				asset, err := requiredString(args, "asset")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 30), "asset", "get", asset, "--json")
			},
		},
		{
			Name:        "aexp_asset_list",
			Description: "List published immutable Asset revisions. Use aexp_asset_get for the complete record.",
			InputSchema: objectSchema(map[string]interface{}{
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.runAexp(ctx, timeoutFromArgs(args, 30), "asset", "list", "--json")
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
			Name:        "aexp_list_workspace_paths",
			Description: "List logical roots that give Agent-visible aexp:// paths stable identities.",
			InputSchema: objectSchema(map[string]interface{}{"workspace": stringSchema("Optional workspace filter."), "timeout": numberSchema("Tool timeout in seconds.")}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolListWorkspacePaths(ctx, args)
			},
		},
		{
			Name:        "aexp_storage_stat",
			Description: "Inspect an aexp://, storage://, or resource:// path. Missing, unreachable, and unknown are returned as explicit states; this does not recursively hash a directory.",
			InputSchema: objectSchema(map[string]interface{}{"uri": stringSchema("aexp://, storage://, or resource:// URI."), "resource": stringSchema("Optional resource for an aexp:// placement."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"uri"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolStorageStat(ctx, args)
			},
		},
		{
			Name:        "aexp_storage_list",
			Description: "List one bounded, non-recursive page from an aexp://, storage://, or resource:// directory. Returns compact name/type/size/mtime entries.",
			InputSchema: objectSchema(map[string]interface{}{"uri": stringSchema("Directory URI."), "resource": stringSchema("Optional resource for an aexp:// placement."), "limit": numberSchema("Maximum entries, capped at 500; default 50."), "cursor": stringSchema("Continue after this entry name."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"uri"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolStorageList(ctx, args)
			},
		},
		{
			Name:        "aexp_storage_locations",
			Description: "Show the primary storage and known cache locations for a path. Freshness describes observation age; revision or manifest hash defines content identity.",
			InputSchema: objectSchema(map[string]interface{}{"uri": stringSchema("aexp://, storage://, or resource:// URI."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"uri"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolStorageLocations(ctx, args)
			},
		},
		{
			Name:        "aexp_storage_copy",
			Description: "Discover the current full SHA-256 source revision and queue a durable verified copy. Returns immediately with a transfer id or structured blockers; never overwrites different destination content.",
			InputSchema: objectSchema(map[string]interface{}{"source": stringSchema("Source aexp://, storage://, resource://, or local:// URI."), "destination": stringSchema("Destination aexp://, storage://, resource://, or local:// URI."), "timeout": numberSchema("Discovery timeout in seconds; large first copies may hash for longer.")}, []string{"source", "destination"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolStorageCopy(ctx, args)
			},
		},
		{
			Name:        "aexp_resolve_path",
			Description: "Resolve an aexp:// logical path to its root-backed candidate placement without claiming the bytes exist.",
			InputSchema: objectSchema(map[string]interface{}{"uri": stringSchema("aexp:// logical URI."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"uri"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolResolvePath(ctx, args)
			},
		},
		{
			Name:        "aexp_inspect_path",
			Description: "Refresh the real remote state of a logical path and return present, missing, unknown, or unreachable distinctly.",
			InputSchema: objectSchema(map[string]interface{}{"uri": stringSchema("aexp:// logical URI."), "resource": stringSchema("Optional placement resource name or id."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"uri"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolInspectPath(ctx, args)
			},
		},
		{
			Name:        "aexp_list_path",
			Description: "List one bounded page of a remote logical directory; never recursively scans the whole NAS.",
			InputSchema: objectSchema(map[string]interface{}{"uri": stringSchema("aexp:// logical URI."), "resource": stringSchema("Optional placement resource name or id."), "limit": numberSchema("Maximum entries, capped at 500."), "cursor": stringSchema("Continue after this entry name."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"uri"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolListPath(ctx, args)
			},
		},
		{
			Name:        "aexp_plan_transfer",
			Description: "Build a side-effect-free cross-Resource transfer plan with route, payload direction, verification, and blockers.",
			InputSchema: objectSchema(transferToolSchema(map[string]interface{}{"timeout": numberSchema("Tool timeout in seconds.")}), []string{"source", "destination"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolPlanTransfer(ctx, args)
			},
		},
		{
			Name:        "aexp_start_transfer",
			Description: "Recompute an accepted plan and create a persistent asynchronous TransferJob.",
			InputSchema: objectSchema(transferToolSchema(map[string]interface{}{"expected_plan_sha256": stringSchema("Plan hash returned by aexp_plan_transfer."), "timeout": numberSchema("Tool timeout in seconds.")}), []string{"source", "destination", "expected_plan_sha256"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolStartTransfer(ctx, args)
			},
		},
		{
			Name:        "aexp_get_transfer",
			Description: "Get TransferJob stage, byte progress, plan identity, and attempt ledger.",
			InputSchema: objectSchema(map[string]interface{}{"transfer_id": stringSchema("Transfer id."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"transfer_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolGetTransfer(ctx, args)
			},
		},
		{
			Name:        "aexp_retry_transfer",
			Description: "Retry a failed or blocked TransferJob without changing its pinned source revision.",
			InputSchema: objectSchema(map[string]interface{}{"transfer_id": stringSchema("Transfer id."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"transfer_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolTransferMutation(ctx, args, "retry")
			},
		},
		{
			Name:        "aexp_cancel_transfer",
			Description: "Cancel a non-terminal TransferJob without deleting its source payload.",
			InputSchema: objectSchema(map[string]interface{}{"transfer_id": stringSchema("Transfer id."), "timeout": numberSchema("Tool timeout in seconds.")}, []string{"transfer_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolTransferMutation(ctx, args, "cancel")
			},
		},
		{
			Name:        "aexp_ensure_path",
			Description: "Create or reuse a persistent verified TransferJob that ensures a logical placement; payload routing never passes through the Agent.",
			InputSchema: objectSchema(transferToolSchema(map[string]interface{}{"expected_plan_sha256": stringSchema("Plan hash returned by aexp_plan_transfer."), "timeout": numberSchema("Tool timeout in seconds.")}), []string{"source", "destination", "expected_plan_sha256"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEnsurePath(ctx, args)
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
			Description: "Run a short bounded inspection command on a resource. Timeout is capped at 45s; use aexp_submit_run for setup, installs, data prep, training, or long commands.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":    stringSchema("Resource name."),
				"command":     stringSchema("Remote command string."),
				"api":         stringSchema("Local aexp API base URL for exec fast path."),
				"cwd":         stringSchema("Optional remote working directory."),
				"project_env": stringSchema("Optional runtime strategy: auto or raw."),
				"conda_env":   stringSchema("Optional conda environment override."),
				"timeout":     numberSchema("Command timeout in seconds, capped at 45."),
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
			Description: "Submit a long-running tracked run owned by a registered Project. Use this for setup, smoke, pilot, formal, and ablation jobs.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":              stringSchema("Resource name."),
				"project_id":            stringSchema("Registered canonical Project id that owns the Run."),
				"command":               stringSchema("Shell command string. Alternative to argv."),
				"argv":                  arrayStringSchema("Structured argv. Alternative to command."),
				"name":                  stringSchema("Run name."),
				"kind":                  stringSchema("Run kind: setup, smoke, pilot, formal, ablation."),
				"cwd":                   stringSchema("Remote working directory."),
				"project_env":           stringSchema("Optional runtime strategy: auto or raw."),
				"conda_env":             stringSchema("Optional conda environment."),
				"target_env":            stringSchema("Semantic target environment, e.g. defect-yolo. Use when setup/repair commands activate or repair a different env than the wrapper env."),
				"gpu_index":             numberSchema("GPU index. -1 means all, -2 means none."),
				"no_gpu":                boolSchema("Do not reserve GPUs or set CUDA_VISIBLE_DEVICES."),
				"shell":                 boolSchema("Interpret argv through bash -lc."),
				"force":                 boolSchema("Skip GPU lock. Requires force_reason."),
				"force_reason":          stringSchema("Required when force or preempt_run is used; explain who/what is being overridden."),
				"preempt_run":           stringSchema("Cancel this active run on the same resource before submitting the new run."),
				"preempt_save":          boolSchema("Whether the preempted run should be treated as needing saved evidence; defaults to true."),
				"refresh_env":           boolSchema("Ignore cached project env detection."),
				"allow_ephemeral_paths": boolSchema("Allow cwd/root_dir that look like temporary mounts; use only for disposable smoke/setup runs."),
				"allow_dirty_git":       boolSchema("Allow a formal/ablation run from a dirty Git worktree."),
				"record_git_diff":       boolSchema("When allowing dirty Git, save a local patch under ~/.aexp/git-diffs."),
				"log_paths":             arrayStringSchema("Log file globs."),
				"metric_paths":          arrayStringSchema("Metric file globs."),
				"artifact_paths":        arrayStringSchema("Artifact file globs."),
				"datasets":              arrayStringSchema("Immutable dataset name@version references."),
				"seeds":                 arrayStringSchema("Explicit integer experiment seeds."),
				"config_sha256":         stringSchema("Launch-time project/config SHA-256."),
				"split_protocol":        stringSchema("Pinned data split protocol for formal/ablation evidence."),
				"evaluation_protocol":   stringSchema("Pinned evaluation/metric protocol for formal/ablation evidence."),
				"inputs": arrayBindingSchema("Managed inputs. Prefer objects with from, to, revision, and optional mode; legacy pipe strings remain accepted.", map[string]interface{}{
					"from": stringSchema("aexp:// logical input URI."), "to": stringSchema("Resource-relative target path."), "revision": stringSchema("Pinned sha256 revision."), "mode": stringSchema("copy (v1)."),
				}, []string{"from", "to", "revision"}),
				"outputs": arrayBindingSchema("Managed outputs. Prefer objects with from, to, role, and required; legacy pipe strings remain accepted.", map[string]interface{}{
					"from": stringSchema("Run-relative artifact path or supported glob."), "to": stringSchema("Durable aexp:// logical URI."), "role": stringSchema("Artifact role."), "required": boolSchema("Whether missing/publish failure blocks finalization."),
				}, []string{"from", "to"}),
				"ui_events":      stringSchema("Structured UI event JSONL path, or off."),
				"launch_timeout": numberSchema("Timeout in seconds for remote launch after the run record is created."),
				"timeout":        numberSchema("MCP tool timeout in seconds."),
			}, []string{"resource", "project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSubmitRun(ctx, args)
			},
		},
		{
			Name:        "aexp_assign_run_project",
			Description: "Explicitly assign or reassign a terminal historical Run to a registered Project. Immutable launch/evidence provenance is not rewritten.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":              stringSchema("Run id."),
				"project_id":          stringSchema("Target registered Project id."),
				"expected_project_id": stringSchema("Optional expected current Project id for CAS. Empty means currently unassigned."),
				"actor":               stringSchema("Audit actor; defaults to agent."),
				"reason":              stringSchema("Reason for changing organizational ownership."),
				"timeout":             numberSchema("Tool timeout in seconds."),
			}, []string{"run_id", "project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolAssignRunProject(ctx, args)
			},
		},
		{
			Name:        "aexp_list_runs",
			Description: "List recent aexp runs as compact summaries; use cursor/limit to avoid flooding agent context.",
			InputSchema: objectSchema(map[string]interface{}{
				"status":     stringSchema("Optional status filter."),
				"resource":   stringSchema("Optional resource name filter."),
				"project":    stringSchema("Optional project id filter."),
				"limit":      numberSchema("Maximum summaries, default 50."),
				"cursor":     numberSchema("Pagination offset returned by the previous call."),
				"trash":      boolSchema("List runs in trash."),
				"deleted":    boolSchema("List logically deleted runs."),
				"important":  boolSchema("Only runs explicitly marked important in the project evidence card."),
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
			Description: "Diagnostic/final status check for one run. Do not use for monitoring loops; prefer aexp_get_run_snapshot for active training progress.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				status, err := s.runAexp(ctx, timeoutFromArgs(args, 20), "run", "status", runID, "--short", "--json")
				if err != nil {
					return "", err
				}
				return addStatusMonitoringHint(runID, status), nil
			},
		},
		{
			Name:        "aexp_get_run_snapshot",
			Description: "Get a low-noise run snapshot: cached status plus latest structured progress, metrics, params, and notes. Prefer this for monitoring instead of repeatedly polling status/logs.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"last":    numberSchema("Number of latest event lines to inspect."),
				"last_n":  numberSchema("Alias for last."),
				"refresh": boolSchema("Refresh remote status before returning. Use sparingly; default false."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunSnapshot(ctx, args)
			},
		},
		{
			Name:        "aexp_tail_run_events",
			Description: "Read the latest structured UI event JSONL entries for a run. Use for progress/metric monitoring before falling back to raw logs.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"last":    numberSchema("Number of latest event lines to read."),
				"last_n":  numberSchema("Alias for last."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolTailRunEvents(ctx, args)
			},
		},
		{
			Name:        "aexp_get_run_metrics",
			Description: "Get latest structured metrics for a run from UI events.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":     stringSchema("Run id."),
				"last":       numberSchema("Number of latest event lines to inspect."),
				"last_n":     numberSchema("Alias for last."),
				"max_issues": numberSchema("Maximum issue details to include; summary keeps total counts."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunMetrics(ctx, args)
			},
		},
		{
			Name:        "aexp_check_run_events",
			Description: "Scan a run's structured UI events for semantic quality problems: long metric names, constants recorded as metrics, epoch gaps, missing trial context, oversized series labels, and shifted loss axes.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"last":    numberSchema("Number of latest event lines to inspect."),
				"last_n":  numberSchema("Alias for last."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunEventQuality(ctx, args)
			},
		},
		{
			Name:        "aexp_latest_run_metrics",
			Description: "Alias for aexp_get_run_metrics. Get latest structured metrics for a run from UI events.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"last":    numberSchema("Number of latest event lines to inspect."),
				"last_n":  numberSchema("Alias for last."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunMetrics(ctx, args)
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
			Name:        "aexp_plan_run_freeze",
			Description: "Deprecated compatibility tool for legacy artifact-only runs. New runs publish outputs through RunIO and use aexp_create_evidence_snapshot.",
			InputSchema: objectSchema(map[string]interface{}{"run_id": stringSchema("Run id."), "profile": stringSchema("Freeze profile, default paper."), "to": stringSchema("storage:// destination."), "workspace": stringSchema("Paper workspace projection path."), "config": stringSchema("Project .aexp.yaml path."), "timeout": numberSchema("Timeout in seconds.")}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				argv := []string{"run", "freeze", runID, "--profile", stringArg(args, "profile", "paper"), "--dry-run", "--json"}
				for _, pair := range [][2]string{{"to", "--to"}, {"workspace", "--workspace"}, {"config", "--config"}} {
					if value := stringArg(args, pair[0], ""); value != "" {
						argv = append(argv, pair[1], value)
					}
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 30), argv...)
			},
		},
		{
			Name:        "aexp_freeze_run",
			Description: "Deprecated compatibility tool that may transport legacy artifacts. New runs use aexp_create_evidence_snapshot after output publication.",
			InputSchema: objectSchema(map[string]interface{}{"run_id": stringSchema("Run id."), "profile": stringSchema("Freeze profile, default paper."), "to": stringSchema("storage:// destination."), "workspace": stringSchema("Paper workspace projection path."), "config": stringSchema("Project .aexp.yaml path."), "timeout": numberSchema("Timeout in seconds.")}, []string{"run_id", "workspace"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				argv := []string{"run", "freeze", runID, "--profile", stringArg(args, "profile", "paper"), "--workspace", stringArg(args, "workspace", ""), "--json"}
				for _, pair := range [][2]string{{"to", "--to"}, {"config", "--config"}} {
					if value := stringArg(args, pair[0], ""); value != "" {
						argv = append(argv, pair[1], value)
					}
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 30), argv...)
			},
		},
		{Name: "aexp_get_freeze", Description: "Inspect a deprecated legacy Freeze record.", InputSchema: objectSchema(map[string]interface{}{"freeze_id": stringSchema("Freeze id."), "timeout": numberSchema("Timeout in seconds.")}, []string{"freeze_id"}), Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
			id, err := requiredString(args, "freeze_id")
			if err != nil {
				return "", err
			}
			return s.runAexp(ctx, timeoutFromArgs(args, 10), "freeze", "status", id, "--json")
		}},
		{Name: "aexp_get_freeze_manifest", Description: "Inspect the file ledger of a deprecated legacy Freeze.", InputSchema: objectSchema(map[string]interface{}{"freeze_id": stringSchema("Freeze id."), "timeout": numberSchema("Timeout in seconds.")}, []string{"freeze_id"}), Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
			id, err := requiredString(args, "freeze_id")
			if err != nil {
				return "", err
			}
			return s.runAexp(ctx, timeoutFromArgs(args, 10), "freeze", "manifest", id)
		}},
		{
			Name:        "aexp_create_evidence_snapshot",
			Description: "Create an idempotent, transport-free Evidence Snapshot from a final RunManifest and verified published output revisions.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 15), "snapshot", "create", runID, "--json")
			},
		},
		{
			Name:        "aexp_get_evidence_snapshot",
			Description: "Get one immutable Evidence Snapshot manifest and its exact published output revision set.",
			InputSchema: objectSchema(map[string]interface{}{
				"snapshot_id": stringSchema("Snapshot id."),
				"timeout":     numberSchema("Timeout in seconds."),
			}, []string{"snapshot_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				id, err := requiredString(args, "snapshot_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 10), "snapshot", "get", id, "--json")
			},
		},
		{
			Name:        "aexp_list_evidence_snapshots",
			Description: "List compact Evidence Snapshot records for one Run.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				runID, err := requiredString(args, "run_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 10), "snapshot", "list", "--run", runID, "--json")
			},
		},
		{
			Name:        "aexp_evaluate_evidence_release",
			Description: "Evaluate an Evidence Snapshot with the Project's single configured aggregate and gate commands. Appends released, blocked, or failed without modifying the Snapshot.",
			InputSchema: objectSchema(map[string]interface{}{
				"snapshot_id": stringSchema("Snapshot id."),
				"timeout":     numberSchema("Timeout in seconds."),
			}, []string{"snapshot_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				id, err := requiredString(args, "snapshot_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 300), "release", "evaluate", id, "--json")
			},
		},
		{
			Name:        "aexp_list_evidence_releases",
			Description: "List immutable Release evaluation events for one Evidence Snapshot.",
			InputSchema: objectSchema(map[string]interface{}{
				"snapshot_id": stringSchema("Snapshot id."),
				"timeout":     numberSchema("Timeout in seconds."),
			}, []string{"snapshot_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				id, err := requiredString(args, "snapshot_id")
				if err != nil {
					return "", err
				}
				return s.runAexp(ctx, timeoutFromArgs(args, 10), "release", "list", "--snapshot", id, "--json")
			},
		},
		{
			Name:        "aexp_archive_run",
			Description: "Move a finished aexp run to trash. Active runs are rejected by aexp.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunLifecycle(ctx, args, "archive")
			},
		},
		{
			Name:        "aexp_restore_run",
			Description: "Restore an aexp run from trash.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunLifecycle(ctx, args, "restore")
			},
		},
		{
			Name:        "aexp_delete_run",
			Description: "Logically delete an aexp run from trash. Remote files are not removed.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":  stringSchema("Run id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolRunLifecycle(ctx, args, "delete")
			},
		},
		{
			Name:        "aexp_create_project_journal_entry",
			Description: "Append a low-friction Markdown work-log entry to a Project. It may cite zero or more Runs, pinned frozen-corpus or Zotero-live literature references, and one concrete next action. Use this for day-to-day reasoning; promote only durable claims to an Evidence Map.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":  stringSchema("Canonical Project id."),
				"actor":       stringSchema("Actor writing the entry; defaults to agent."),
				"title":       stringSchema("Short work-log title."),
				"body_md":     stringSchema("Markdown body with reasoning, observations, or decisions."),
				"next_action": stringSchema("Optional single concrete next action."),
				"run_ids":     arrayStringSchema("Optional related Run ids. Every Run must belong to this Project."),
				"literature_refs": map[string]interface{}{
					"type":        "array",
					"description": "Optional pinned Zotero citations: frozen_corpus refs returned by aexp_literature_query, or zotero_live refs obtained through the existing Zotero MCP with item and library versions.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"source_kind":     stringSchema("frozen_corpus or zotero_live."),
							"zotero_item_key": stringSchema("Stable Zotero item key."),
							"zotero_uri":      stringSchema("Zotero deep link."),
							"page_label":      stringSchema("Optional page label."),
							"corpus_revision": stringSchema("Required for frozen_corpus."),
							"chunk_sha256":    stringSchema("Required for frozen_corpus."),
							"item_version":    numberSchema("Required for zotero_live."),
							"library_version": numberSchema("Required for zotero_live."),
						},
						"required": []string{"source_kind", "zotero_item_key", "zotero_uri"},
					},
				},
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"project_id", "title"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolCreateProjectJournalEntry(ctx, args)
			},
		},
		{
			Name:        "aexp_list_project_journal",
			Description: "List a Project work journal newest first. Defaults to compact JSON and may filter by Run, text, or next-action status.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":         stringSchema("Canonical Project id."),
				"run_id":             stringSchema("Optional related Run id."),
				"query":              stringSchema("Optional title/body/next-action search."),
				"next_action_status": stringSchema("Optional next action filter: none, open, or done."),
				"limit":              numberSchema("Maximum entries."),
				"timeout":            numberSchema("Tool timeout in seconds."),
			}, []string{"project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolListProjectJournal(ctx, args)
			},
		},
		{
			Name:        "aexp_get_project_journal_entry",
			Description: "Read one full Project journal entry including Markdown body, related Runs, literature references, and next action.",
			InputSchema: objectSchema(map[string]interface{}{
				"entry_id": stringSchema("Project journal entry id."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, []string{"entry_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolGetProjectJournalEntry(ctx, args)
			},
		},
		{
			Name:        "aexp_update_project_journal_next_action",
			Description: "Mark the explicit next action on a Project journal entry open or done without rewriting the append-only journal body.",
			InputSchema: objectSchema(map[string]interface{}{
				"entry_id": stringSchema("Project journal entry id."),
				"status":   stringSchema("Next action status: open or done."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, []string{"entry_id", "status"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolUpdateProjectJournalNextAction(ctx, args)
			},
		},
		{
			Name:        "aexp_mark_run",
			Description: "Legacy compatibility tool: attach a Markdown note to exactly one Run. Prefer aexp_create_project_journal_entry for new research reasoning.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":     stringSchema("Run id."),
				"actor":      stringSchema("Actor writing the mark."),
				"kind":       stringSchema("Mark kind, e.g. key_result, failure, note, followup."),
				"title":      stringSchema("Short note title."),
				"statement":  stringSchema("One-sentence statement shown in mark lists."),
				"body_md":    stringSchema("Markdown body shown when the note is opened. Use ordinary Markdown. For attached images use ![caption](aexp-attachment://attachment_id); if omitted, aexp appends refs for provided attachments."),
				"reason":     stringSchema("Legacy short reason. Prefer statement/body_md for new notes."),
				"evidence":   stringSchema("Legacy evidence text or paths. Prefer body_md plus attachment for new notes."),
				"attachment": arrayStringSchema("Local file/image path to copy into the mark, repeatable. Syntax: PATH or PATH|caption."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolMarkRun(ctx, args)
			},
		},
		{
			Name:        "aexp_list_run_marks",
			Description: "Legacy read-only compatibility tool: list historical RunMark records. Use Project journal tools for current research reasoning.",
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
			Name:        "aexp_list_evidence_chains",
			Description: "List Evidence Chain boards as JSON. Use this to find a reasoning board before reading or linking nodes.",
			InputSchema: objectSchema(map[string]interface{}{
				"query":   stringSchema("Optional search over chain id, title, or description."),
				"limit":   numberSchema("Maximum chains to return."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceList(ctx, args)
			},
		},
		{
			Name:        "aexp_list_project_evidence_graphs",
			Description: "List compact routing metadata for one Project's evidence Maps. Use this before routing a proposal. Select an existing Topic when it fits, create a Topic when needed, or keep an unrouted draft; never use Primary as a fallback.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id": stringSchema("Canonical Project id."),
				"status":     stringSchema("Graph status; defaults to active."),
				"limit":      numberSchema("Maximum graphs, capped at 50."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectEvidenceGraphs(ctx, args)
			},
		},
		{
			Name:        "aexp_create_topic_evidence_graph",
			Description: "Create an active topic evidence graph inside a registered Project. Purpose explains its research scope; recipes and keywords are compact Agent routing hints.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id": stringSchema("Canonical Project id."),
				"title":      stringSchema("Short topic graph title."),
				"purpose":    stringSchema("What research question or evidence belongs in this graph."),
				"recipes":    arrayStringSchema("Recipe names that route here when uniquely matched."),
				"keywords":   arrayStringSchema("Fallback keywords that describe this graph's scope."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"project_id", "title", "purpose"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolCreateTopicEvidenceGraph(ctx, args)
			},
		},
		{
			Name:        "aexp_create_evidence_chain",
			Description: "Legacy compatibility tool for creating a secondary Evidence Chain. Prefer aexp_create_topic_evidence_graph with explicit purpose and routing hints.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":  stringSchema("Canonical Project id."),
				"title":       stringSchema("Evidence Chain title."),
				"description": stringSchema("Short description of the research question or reasoning scope."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, []string{"project_id", "title"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceCreate(ctx, args)
			},
		},
		{
			Name:        "aexp_get_evidence_chain",
			Description: "Read the full compatibility/advanced raw Evidence Map snapshot, including node data_json and the accepted Research Thread projection. Prefer aexp_get_evidence_thread_map for normal Topic reading; use run tools for detailed run logs/metrics.",
			InputSchema: objectSchema(map[string]interface{}{
				"chain_id": stringSchema("Evidence Chain id."),
				"timeout":  numberSchema("Tool timeout in seconds."),
			}, []string{"chain_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceGet(ctx, args)
			},
		},
		{
			Name:        "aexp_get_evidence_thread_map",
			Description: "Read one accepted Topic using the research-thread-v2 contract as Research Threads arranged into fixed Stage Columns before routing or authoring Evidence. presentation_stage_order is authoritative. Returns research-health-v2 advisory diagnostics, Result interpretations, parent/child Threads, capacity/thread_families (including presentation-only split_recommended), unassigned reasons, revision, and graph hash. Never invent a split from titles or move evidence automatically. This does not run provenance audit or mutate the Map.",
			InputSchema: objectSchema(map[string]interface{}{
				"map_id":  stringSchema("Evidence Map id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"map_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceThreadMap(ctx, args)
			},
		},
		{
			Name:        "aexp_audit_evidence_map",
			Description: "Audit the accepted Evidence Map after user acceptance. Returns hard eligibility/blockers plus separate readability, v2 compliance, publication status, and the same research-health-v2 advisory snapshot. It cannot audit an unaccepted proposal; use proposal-plan first. Legacy visual Run nodes remain compatibility warnings and are never rewritten.",
			InputSchema: objectSchema(map[string]interface{}{
				"map_id":  stringSchema("Evidence Map id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"map_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceAudit(ctx, args)
			},
		},
		{
			Name:        "aexp_archive_evidence_map",
			Description: "Archive one secondary Topic Map while preserving nodes, edges, revisions and hashes. Primary Maps cannot be archived. This never permanently deletes evidence.",
			InputSchema: objectSchema(map[string]interface{}{
				"map_id":  stringSchema("Secondary Topic Map id."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, []string{"map_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolArchiveEvidenceMap(ctx, args)
			},
		},
		{
			Name:        "aexp_create_evidence_proposal",
			Description: "Create a reviewable research-thread-v2 Evidence Proposal; it never accepts the change. Read aexp_get_evidence_thread_map first. Submit hypothesis, experiment design, Result provenance/disposition, and durable Conclusion and/or Issue semantics; Interpretation is server-derived, not a node. Stable negative evidence belongs in a Conclusion, while an unresolved data/protocol/implementation limitation belongs in an Issue. Route to an explicit matching Topic or leave a draft; never use Primary as fallback.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id":           stringSchema("Canonical Project id."),
				"target_map_id":        stringSchema("Explicit target Map id. Omit only for an unrouted draft."),
				"actor":                stringSchema("Proposal author identity; defaults to agent."),
				"summary":              stringSchema("Short human-readable proposal summary."),
				"routing_reason":       stringSchema("Why this Topic Map owns the proposed change."),
				"project_level_impact": boolSchema("Required true for a direct Primary Map proposal."),
				"source_run_ids":       arrayStringSchema("Optional source Runs; zero Runs is valid for bootstrap context."),
				"source_snapshot_ids":  arrayStringSchema("Optional immutable Evidence Snapshot sources."),
				"patch_json":           stringSchema("Reviewable patch JSON with nodes and typed edges. Agent coordinates are ignored. Use type=experiment for an experiment design. A result node uses type=claim, data_json.claimKind=result, resultDisposition=conclusion|issue|mixed|pending, and node-level source_run_ids/source_snapshot_ids. Every new or touched Result requires an incoming Experiment Design --next_step--> Result edge; the complete ownership spine is Hypothesis --next_step--> Design --next_step--> Result. conclusion requires only outgoing Result --supports/weakens/does_not_prove--> Conclusion edges; issue requires only outgoing Result --reveals_issue--> Issue edges; mixed requires both; pending requires neither. A Result must not emit an outgoing semantic edge back to a Hypothesis, design, or another Result. issue/mixed/pending require dispositionReason unless the reveals_issue edge itself has a rationale. Interpretation cards are read-only UI/MCP projections and must never be submitted as nodes. Reference Run, dataset and config identity as provenance rather than graph containers. Do not create protocol groups. Optional layout_intent remains left-to-right semantic intent only."),
				"timeout":              numberSchema("Tool timeout in seconds."),
			}, []string{"project_id", "summary", "patch_json"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalSubmit(ctx, args)
			},
		},
		{
			Name:        "aexp_branch_from_outcome",
			Description: "Create and immediately plan a reviewable child-thread proposal from one accepted Conclusion or Issue in a Topic Map. Read aexp_get_evidence_thread_map first. The server generates Conclusion/Issue --next_step--> canonical child Hypothesis and, optionally, Hypothesis --next_step--> Experiment Design. It never accepts the proposal, never writes coordinates, and exposes no raw patch_json. The returned next_action is user_review when the plan is eligible.",
			InputSchema: objectSchema(map[string]interface{}{
				"map_id":                    stringSchema("Active secondary Topic Map id."),
				"outcome_node_id":           stringSchema("Accepted Conclusion or Issue node that motivates the branch."),
				"hypothesis_title":          stringSchema("Testable child hypothesis title."),
				"hypothesis_body_md":        stringSchema("Optional Markdown explanation of the child hypothesis."),
				"branch_rationale":          stringSchema("Why this accepted outcome motivates the new hypothesis."),
				"experiment_design_title":   stringSchema("Optional first experiment-design title."),
				"experiment_design_body_md": stringSchema("Optional Markdown design details; requires experiment_design_title."),
				"summary":                   stringSchema("Optional proposal summary; derived from the hypothesis when omitted."),
				"actor":                     stringSchema("Proposal author identity; defaults to agent."),
				"timeout":                   numberSchema("Tool timeout in seconds."),
			}, []string{"map_id", "outcome_node_id", "hypothesis_title", "branch_rationale"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceBranchFromOutcome(ctx, args)
			},
		},
		{
			Name:        "aexp_list_evidence_proposals",
			Description: "List independent Evidence Proposals for one Project, including draft, pending, accepted, rejected, expired, and conflicted history.",
			InputSchema: objectSchema(map[string]interface{}{
				"project_id": stringSchema("Canonical Project id."),
				"status":     stringSchema("Optional proposal status filter."),
				"limit":      numberSchema("Maximum proposals."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"project_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalList(ctx, args)
			},
		},
		{
			Name:        "aexp_get_evidence_proposal",
			Description: "Read one independent Evidence Proposal by proposal id.",
			InputSchema: objectSchema(map[string]interface{}{
				"proposal_id": stringSchema("Independent Evidence Proposal id."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, []string{"proposal_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalGet(ctx, args)
			},
		},
		{
			Name:        "aexp_reroute_evidence_proposal",
			Description: "Create a revision-aware replacement of a draft or pending Proposal on another explicit Map. The prior Proposal is retained as expired history.",
			InputSchema: objectSchema(map[string]interface{}{
				"proposal_id":          stringSchema("Independent Evidence Proposal id."),
				"target_map_id":        stringSchema("New explicit target Map id."),
				"routing_reason":       stringSchema("Why the selected Topic Map owns the change."),
				"project_level_impact": boolSchema("Required true when rerouting directly to Primary."),
				"timeout":              numberSchema("Tool timeout in seconds."),
			}, []string{"proposal_id", "target_map_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalReroute(ctx, args)
			},
		},
		{
			Name:        "aexp_plan_evidence_promotion",
			Description: "Plan a concise Topic-to-Primary promotion without side effects. It pins the accepted Topic revision/hash and proposes one project-level summary plus one navigable Map Reference.",
			InputSchema: objectSchema(map[string]interface{}{
				"source_map_id":   stringSchema("Accepted Topic Map id."),
				"source_node_ids": arrayStringSchema("Accepted Topic node ids summarized by this promotion."),
				"summary":         stringSchema("Concise project-level claim, issue, or plan."),
				"node_type":       stringSchema("claim, issue, or plan; defaults to claim."),
				"actor":           stringSchema("Proposal author identity."),
				"timeout":         numberSchema("Tool timeout in seconds."),
			}, []string{"source_map_id", "summary"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidencePromotionPlan(ctx, args)
			},
		},
		{
			Name:        "aexp_create_evidence_promotion",
			Description: "Create the independent Primary Proposal described by an unchanged promotion plan. This does not accept it; the user still reviews the Proposal.",
			InputSchema: objectSchema(map[string]interface{}{
				"source_map_id":      stringSchema("Accepted Topic Map id."),
				"source_node_ids":    arrayStringSchema("Accepted Topic node ids summarized by this promotion."),
				"summary":            stringSchema("Concise project-level claim, issue, or plan."),
				"node_type":          stringSchema("claim, issue, or plan; defaults to claim."),
				"actor":              stringSchema("Proposal author identity."),
				"expected_plan_hash": stringSchema("Exact plan_hash returned by aexp_plan_evidence_promotion."),
				"timeout":            numberSchema("Tool timeout in seconds."),
			}, []string{"source_map_id", "summary", "expected_plan_hash"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidencePromotionCreate(ctx, args)
			},
		},
		{
			Name:        "aexp_propose_evidence_graph",
			Description: "Legacy Run Card proposal adapter. Prefer aexp_create_evidence_proposal. This compatibility tool still requires a Run and must not be used to bootstrap a graph.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":          stringSchema("Run id whose Project Run Card owns the proposal."),
				"graph_id":        stringSchema("Target evidence graph id. Omit only to use the Run Project's primary graph."),
				"routing_reason":  stringSchema("Why the selected topic graph is the unique or clearly best match."),
				"patch_json":      stringSchema("Reviewable patch JSON with nodes and edges. Agent coordinates are ignored. Optional layout_intent uses left-to-right ranks: {\"flow\":\"left_to_right\",\"ranks\":[[\"node_id\"]],\"rationale\":\"...\"}; list protocol containers rather than their members."),
				"base_revision":   numberSchema("Optional expected graph revision; otherwise the current selected graph revision is resolved."),
				"no_graph_impact": boolSchema("Record that this run does not change the research graph."),
				"reason":          stringSchema("Required explanation when no_graph_impact is true."),
				"timeout":         numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProposeEvidenceGraph(ctx, args)
			},
		},
		{
			Name:        "aexp_propose_evidence_graph_patch",
			Description: "Legacy Run Card proposal tool. Prefer aexp_create_evidence_proposal; this compatibility path cannot bootstrap a graph without a Run.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":          stringSchema("Run id whose Project Run Card owns the proposal."),
				"chain_id":        stringSchema("Optional explicit graph id. Omit to use the Run project's active primary Evidence Map."),
				"patch_json":      stringSchema("Reviewable patch JSON with nodes and edges. Agent coordinates are ignored. Optional layout_intent uses left-to-right ranks: {\"flow\":\"left_to_right\",\"ranks\":[[\"node_id\"]],\"rationale\":\"...\"}; list protocol containers rather than their members."),
				"base_revision":   numberSchema("Revision for an explicit chain override. The project primary resolves its current revision automatically."),
				"no_graph_impact": boolSchema("Record that this run does not change the research graph."),
				"reason":          stringSchema("Required explanation when no_graph_impact is true."),
				"routing_reason":  stringSchema("Why an explicitly selected topic graph is the correct target."),
				"timeout":         numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidencePropose(ctx, args)
			},
		},
		{
			Name:        "aexp_plan_evidence_graph_proposal",
			Description: "Plan proposal acceptance without side effects. Returns revision/provenance/thread-contract blockers, advisory warnings, and projected_research for the candidate overlay. Before review, verify eligible=true, projected_research.unassigned is empty for every touched semantic node, and the intended hypothesis Thread contains the expected Result. A new or touched Result without Design --next_step--> Result is blocked. Warnings guide Agent authoring but do not prevent acceptance; blockers do.",
			InputSchema: objectSchema(map[string]interface{}{
				"proposal_id": stringSchema("Independent Evidence Proposal id (preferred)."),
				"run_id":      stringSchema("Legacy Run id owning an old Project Run Card proposal."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalPlan(ctx, args)
			},
		},
		{
			Name:        "aexp_plan_evidence_reorganization",
			Description: "Plan one bounded semantic cleanup inside an existing Evidence Map without side effects. Supply upserts/deletes/typed edges, not pixel coordinates. The result includes before/after research threads, blockers, warnings and a revision-bound plan_hash. Cross-Map moves must be split into separately reviewed proposals.",
			InputSchema: objectSchema(map[string]interface{}{
				"map_id":     stringSchema("Target Evidence Map id."),
				"patch_json": stringSchema("Bounded EvidenceGraphPatch JSON using node/edge ids, upserts and deletes. Keep each proposal to roughly 8-12 semantic nodes."),
				"timeout":    numberSchema("Tool timeout in seconds."),
			}, []string{"map_id", "patch_json"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceReorganizationPlan(ctx, args)
			},
		},
		{
			Name:        "aexp_create_evidence_reorganization_proposal",
			Description: "Create the reviewable Evidence Proposal described by an unchanged reorganization plan. This never directly edits the Map; the user still accepts or rejects it.",
			InputSchema: objectSchema(map[string]interface{}{
				"map_id":             stringSchema("Target Evidence Map id."),
				"summary":            stringSchema("Short semantic cleanup summary."),
				"patch_json":         stringSchema("Exact patch_json used for planning."),
				"expected_plan_hash": stringSchema("Exact plan_hash returned by aexp_plan_evidence_reorganization."),
				"actor":              stringSchema("Proposal author identity; defaults to agent."),
				"routing_reason":     stringSchema("Why the selected Map owns this cleanup."),
				"source_run_ids":     arrayStringSchema("Optional Run ids referenced by the cleanup."),
				"timeout":            numberSchema("Tool timeout in seconds."),
			}, []string{"map_id", "summary", "patch_json", "expected_plan_hash"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceReorganizationCreate(ctx, args)
			},
		},
		{
			Name:        "aexp_rebase_evidence_proposal",
			Description: "Create a current-revision replacement for a stale pending Evidence Proposal only when every touched node and edge is unchanged. The old Proposal becomes expired history; semantic conflicts are reported precisely.",
			InputSchema: objectSchema(map[string]interface{}{
				"proposal_id": stringSchema("Pending Evidence Proposal id."),
				"actor":       stringSchema("Actor recorded on the replacement Proposal."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, []string{"proposal_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalRebase(ctx, args)
			},
		},
		{
			Name:        "aexp_review_evidence_graph_proposal",
			Description: "Accept, reject, or expire an independent Evidence Proposal. Acceptance is revision-checked and transactionally updates the target Map. run_id remains only for legacy Run Card proposals.",
			InputSchema: objectSchema(map[string]interface{}{
				"proposal_id": stringSchema("Independent Evidence Proposal id (preferred)."),
				"run_id":      stringSchema("Legacy Run id owning an old Project Run Card proposal."),
				"action":      stringSchema("accept, reject, or expire."),
				"reviewer":    stringSchema("Reviewer identity recorded in the graph revision."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, []string{"action"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolEvidenceProposalReview(ctx, args)
			},
		},
		{
			Name:        "aexp_list_matrices",
			Description: "List Experiment Matrices as JSON. Matrices are plain experiment tables, not project containers.",
			InputSchema: objectSchema(map[string]interface{}{
				"query":   stringSchema("Optional search over matrix id, title, or description."),
				"limit":   numberSchema("Maximum matrices to return."),
				"timeout": numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolMatrixList(ctx, args)
			},
		},
		{
			Name:        "aexp_create_matrix",
			Description: "Create an Experiment Matrix table. Use rows as experiments/runs and columns as aligned fields or metrics.",
			InputSchema: objectSchema(map[string]interface{}{
				"title":       stringSchema("Matrix title."),
				"description": stringSchema("Optional matrix scope/question."),
				"columns":     arrayStringSchema("Initial column labels, e.g. run_id, val_loss, test_mse, conclusion."),
				"no_defaults": boolSchema("Create with no default row/columns."),
				"timeout":     numberSchema("Tool timeout in seconds."),
			}, []string{"title"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolMatrixCreate(ctx, args)
			},
		},
		{
			Name:        "aexp_get_matrix",
			Description: "Read one Experiment Matrix as JSON, including rows, columns, cells, run ids, card ids, metrics, and conclusions.",
			InputSchema: objectSchema(map[string]interface{}{
				"matrix_id": stringSchema("Experiment Matrix id."),
				"timeout":   numberSchema("Tool timeout in seconds."),
			}, []string{"matrix_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolMatrixGet(ctx, args)
			},
		},
		{
			Name:        "aexp_set_matrix_cell",
			Description: "Set one Experiment Matrix cell by row label and column label. Missing rows/columns are created. Use run_id to attach an experiment; never edit SQLite directly.",
			InputSchema: objectSchema(map[string]interface{}{
				"matrix_id":       stringSchema("Experiment Matrix id."),
				"row":             stringSchema("Experiment row label, e.g. trial022 seed2021."),
				"column":          stringSchema("Column label, e.g. run_id, val_loss, conclusion."),
				"value":           stringSchema("Cell value. For run_id columns this may be the run id."),
				"run_id":          stringSchema("Run id to attach to this cell."),
				"project_card_id": stringSchema("Optional project card id for the run."),
				"timeout":         numberSchema("Tool timeout in seconds."),
			}, []string{"matrix_id", "row", "column"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolMatrixSetCell(ctx, args)
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
				"recipe":                stringSchema("Project recipe name, defaults to train."),
				"name":                  stringSchema("Alias for recipe."),
				"run_name":              stringSchema("Override run name."),
				"config":                stringSchema("Optional project config path."),
				"resource":              stringSchema("Override resource name."),
				"cwd":                   stringSchema("Override working directory."),
				"kind":                  stringSchema("Override run kind."),
				"project_env":           stringSchema("Override runtime env strategy: auto or raw."),
				"conda_env":             stringSchema("Override conda environment."),
				"target_env":            stringSchema("Override semantic target environment, e.g. the env a setup recipe creates or repairs."),
				"ui_events":             stringSchema("Override structured UI event JSONL path; set off to disable."),
				"gpu_index":             numberSchema("Override GPU index."),
				"no_gpu":                boolSchema("Do not reserve GPUs or set CUDA_VISIBLE_DEVICES."),
				"force":                 boolSchema("Skip GPU slot lock. Requires force_reason."),
				"force_reason":          stringSchema("Required when force or preempt_run is used; explain why the GPU lock was bypassed or who is being preempted."),
				"preempt_run":           stringSchema("Cancel this active run on the same resource before submitting the recipe."),
				"preempt_save":          boolSchema("Whether the preempted run should be treated as needing saved evidence; defaults to true."),
				"dry_run":               boolSchema("Print the resolved submit command without launching."),
				"refresh_env":           boolSchema("Ignore cached project profile and re-detect the environment."),
				"allow_ephemeral_paths": boolSchema("Allow cwd/root_dir that look like temporary mounts; use only for disposable smoke/setup runs."),
				"allow_dirty_git":       boolSchema("Allow a formal/ablation recipe from a dirty Git worktree."),
				"record_git_diff":       boolSchema("When allowing dirty Git, save a local patch under ~/.aexp/git-diffs."),
				"launch_timeout":        numberSchema("Launch timeout in seconds."),
				"timeout":               numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectRun(ctx, args)
			},
		},
		{
			Name:        "aexp_project_card",
			Description: "Legacy compatibility tool: create or update a project-level card for one Run. Prefer aexp_create_project_journal_entry for current research reasoning.",
			InputSchema: objectSchema(map[string]interface{}{
				"run_id":           stringSchema("Run id."),
				"config":           stringSchema("Optional project config path."),
				"question":         stringSchema("What this run was meant to answer."),
				"verdict":          stringSchema("One-sentence conclusion."),
				"level":            stringSchema("Evidence level: A, B, or C."),
				"metric":           arrayStringSchema("Key metric line, repeatable."),
				"artifact":         arrayStringSchema("Artifact path, repeatable."),
				"supports":         stringSchema("Claim this run supports."),
				"weakens":          stringSchema("Claim this run weakens."),
				"next_action":      stringSchema("Recommended next action."),
				"important":        boolSchema("Mark this run as important for project review."),
				"promote":          boolSchema("Mark this card as worth promoting to notes/proposal."),
				"reassign_project": boolSchema("Explicitly move an existing card to the project declared by config."),
				"proposal_reason":  stringSchema("Why this deserves promotion."),
				"related_run":      arrayStringSchema("Related run id, repeatable."),
				"timeout":          numberSchema("Tool timeout in seconds."),
			}, []string{"run_id"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectCard(ctx, args)
			},
		},
		{
			Name:        "aexp_project_runs",
			Description: "Legacy read-only compatibility tool: list project-level Run cards. Prefer aexp_list_project_journal for current project memory.",
			InputSchema: objectSchema(map[string]interface{}{
				"config":    stringSchema("Optional project config path."),
				"important": boolSchema("Show only important cards."),
				"limit":     numberSchema("Maximum number of cards."),
				"timeout":   numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectRuns(ctx, args)
			},
		},
		{
			Name:        "aexp_project_digest",
			Description: "Legacy read-only compatibility tool: read a digest of project Run cards. Prefer aexp_list_project_journal for current project memory.",
			InputSchema: objectSchema(map[string]interface{}{
				"config":    stringSchema("Optional project config path."),
				"important": boolSchema("Show only important cards."),
				"limit":     numberSchema("Maximum number of cards."),
				"json":      boolSchema("Output JSON instead of Markdown text."),
				"timeout":   numberSchema("Tool timeout in seconds."),
			}, nil),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolProjectDigest(ctx, args)
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
			Description: "Push local files to a resource with rsync; falls back to an SSH tar stream when remote rsync is unavailable.",
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
			Name:        "aexp_sync_dataset_push",
			Description: "Push dataset/output directories with data-safe rsync defaults: partial transfers, progress, retries, and checksum verification. Use this instead of ad-hoc rsync for first-class experiment data sync.",
			InputSchema: objectSchema(map[string]interface{}{
				"resource":     stringSchema("Resource name."),
				"source":       stringSchema("Local dataset/output directory."),
				"target":       stringSchema("Remote target directory."),
				"dry_run":      boolSchema("Print the rsync command without running it."),
				"delete":       boolSchema("Delete target files that no longer exist on source."),
				"verify":       boolSchema("Use checksum verification; defaults to true."),
				"exclude":      arrayStringSchema("Extra exclude patterns."),
				"sync_timeout": numberSchema("Rsync timeout in seconds."),
				"retries":      numberSchema("Retry count for transient rsync failures."),
				"timeout":      numberSchema("Tool timeout in seconds."),
			}, []string{"resource", "source", "target"}),
			Handler: func(s *Server, ctx context.Context, args map[string]interface{}) (string, error) {
				return s.toolSyncDatasetPush(ctx, args)
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
			Description: "Run an aexp CLI command through MCP. Mutating commands are allowed; long-lived serve/mcp and unbounded --follow are blocked.",
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
	base["remote_path"] = stringSchema("PATH prefix for non-interactive remote commands, e.g. /opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin.")
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

func transferToolSchema(base map[string]interface{}) map[string]interface{} {
	base["source"] = stringSchema("Source aexp://, storage://, resource://, or local:// URI.")
	base["destination"] = stringSchema("Destination aexp://, storage://, resource://, or local:// URI.")
	base["source_revision"] = stringSchema("Pinned source SHA-256 revision when not supplied by a verified Placement.")
	base["initiator"] = stringSchema("Transfer initiator: auto, nas, compute, or mac.")
	base["verification"] = stringSchema("Destination verification: sha256, manifest, or none.")
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
	base["series"] = stringSchema("Series/context label for grouping related events. For sweeps, prefer a stable label such as trial:7 or use trial.")
	base["run"] = stringSchema("Run or sub-run label for grouping related events.")
	base["variant"] = stringSchema("Variant label, e.g. model, ablation, or data condition.")
	base["split"] = stringSchema("Data split label, e.g. train, val, or test.")
	base["stage"] = stringSchema("Stage label, e.g. setup, train, eval, or cleanup.")
	base["label"] = stringSchema("Short human display label.")
	base["trial"] = stringSchema("Trial id/label for hyperparameter search; the UI treats it as a separate series.")
	base["seed"] = stringSchema("Seed id/label; the UI treats it as part of the series identity.")
	base["fold"] = stringSchema("Fold id/label; the UI treats it as part of the series identity.")
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

func arrayBindingSchema(description string, properties map[string]interface{}, required []string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "array",
		"description": description,
		"items": map[string]interface{}{"oneOf": []interface{}{
			objectSchema(properties, required),
			map[string]interface{}{"type": "string", "description": "Legacy pipe-delimited compatibility form."},
		}},
	}
}

func mapSchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"description":          description,
		"additionalProperties": true,
	}
}
