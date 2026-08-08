package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ziwu/aexp/internal/store"
)

const maxCapturedOutput = 128 * 1024

type Service struct {
	Store store.Store
}

type BlockedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *BlockedError) Error() string { return e.Message }

type commandResult struct {
	Command  string `json:"command,omitempty"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// Evaluate runs the one aggregate command and one gate command owned by the
// Snapshot's Project, then appends one immutable Release event. It never
// changes the Snapshot or accepts a caller-supplied release decision.
func (s Service) Evaluate(ctx context.Context, snapshotID string) (*store.EvidenceRelease, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("release store is unavailable")
	}
	snapshot, err := s.Store.GetEvidenceSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, &BlockedError{Code: "snapshot_not_found", Message: "Evidence Snapshot does not exist"}
	}
	project, err := s.Store.GetProjectDefinition(ctx, snapshot.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, &BlockedError{Code: "project_not_found", Message: "Snapshot Project does not exist"}
	}
	run, err := s.Store.GetRun(ctx, snapshot.RunID)
	if err != nil {
		return nil, err
	}
	readiness, err := store.CheckRunClaimReadiness(ctx, s.Store, run)
	if err != nil {
		return nil, err
	}
	if len(readiness) > 0 {
		gateJSON, _ := json.Marshal(map[string]interface{}{"blockers": readiness})
		release := &store.EvidenceRelease{
			SnapshotID: snapshot.ID, ProjectID: snapshot.ProjectID, State: store.EvidenceReleaseBlocked,
			AggregateResultJSON: `{"skipped":true}`, GateResultJSON: string(gateJSON),
			ErrorCode: "provenance_blocked", LastError: "Formal claim provenance is incomplete",
		}
		if err := s.Store.AppendEvidenceRelease(ctx, release); err != nil {
			return nil, err
		}
		return release, nil
	}
	aggregateCommand := strings.TrimSpace(project.AggregateCommand)
	gateCommand := strings.TrimSpace(project.GateCommand)
	root := filepath.Clean(strings.TrimSpace(project.LocalRoot))
	// Formal provenance, final RunManifest, and verified published outputs are
	// the built-in Release gate. A Project local root is only needed when the
	// Project opts into additional aggregate or gate hooks.
	if aggregateCommand != "" || gateCommand != "" {
		if root == "" || !filepath.IsAbs(root) {
			return nil, &BlockedError{Code: "project_root_missing", Message: "Project local_root must be an absolute directory before running Release hooks"}
		}
		info, statErr := os.Stat(root)
		if statErr != nil || !info.IsDir() {
			return nil, &BlockedError{Code: "project_root_unavailable", Message: "Project local_root is not an accessible directory"}
		}
	}

	env := append(os.Environ(),
		"AEXP_SNAPSHOT_ID="+snapshot.ID,
		"AEXP_SNAPSHOT_MANIFEST_SHA256="+snapshot.ManifestSHA256,
		"AEXP_SNAPSHOT_MANIFEST_JSON="+snapshot.ManifestJSON,
		"AEXP_PROJECT_ID="+snapshot.ProjectID,
		"AEXP_RUN_ID="+snapshot.RunID,
	)
	aggregate := commandResult{Skipped: true}
	state := store.EvidenceReleaseReleased
	errorCode := ""
	lastError := ""
	if aggregateCommand != "" {
		aggregate = executeCommand(ctx, root, env, aggregateCommand)
		if aggregate.ExitCode != 0 {
			state = store.EvidenceReleaseFailed
			errorCode = "aggregate_failed"
			lastError = "Project aggregate command failed"
		}
	}

	gate := commandResult{Skipped: true}
	if state != store.EvidenceReleaseFailed && gateCommand != "" {
		gate = executeCommand(ctx, root, env, gateCommand)
		if gate.ExitCode != 0 {
			if ctx.Err() != nil {
				state = store.EvidenceReleaseFailed
				errorCode = "gate_execution_failed"
				lastError = ctx.Err().Error()
			} else {
				state = store.EvidenceReleaseBlocked
				errorCode = "gate_blocked"
				lastError = "Release gate rejected this Snapshot"
			}
		}
	}
	aggregateJSON, _ := json.Marshal(aggregate)
	gateJSON, _ := json.Marshal(gate)
	release := &store.EvidenceRelease{
		SnapshotID:          snapshot.ID,
		ProjectID:           snapshot.ProjectID,
		State:               state,
		AggregateResultJSON: string(aggregateJSON),
		GateResultJSON:      string(gateJSON),
		ErrorCode:           errorCode,
		LastError:           lastError,
	}
	if err := s.Store.AppendEvidenceRelease(ctx, release); err != nil {
		return nil, err
	}
	return release, nil
}

func executeCommand(ctx context.Context, root string, env []string, command string) commandResult {
	result := commandResult{Command: command}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", command)
	cmd.Dir = root
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if len(output) > maxCapturedOutput {
		output = output[len(output)-maxCapturedOutput:]
	}
	result.Output = string(output)
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	result.ExitCode = -1
	if result.Output == "" {
		result.Output = err.Error()
	}
	return result
}
