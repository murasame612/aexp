package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/ziwu/aexp/internal/store"
)

const (
	ProjectEnvAuto  = "auto"
	ProjectEnvRaw   = "raw"
	ProjectEnvVenv  = "venv"
	ProjectEnvUV    = "uv"
	ProjectEnvConda = "conda"
)

// DetectProject resolves the runtime profile for a project directory on a resource.
func (e *Executor) DetectProject(ctx context.Context, resource *store.Resource, cwd, strategy, condaEnv string) (*store.ProjectProfile, error) {
	if strategy == "" {
		strategy = ProjectEnvAuto
	}
	if strategy != ProjectEnvAuto && strategy != ProjectEnvRaw {
		return nil, fmt.Errorf("project env strategy must be raw or auto")
	}
	resolvedCwd := resolveRemoteCwd(resource.RootDir, cwd)
	if cwd == "" {
		cwd = resolvedCwd
	}
	if condaEnv == "" {
		condaEnv = resource.CondaEnv
	}

	profile := &store.ProjectProfile{
		ResourceID:   resource.ID,
		ResourceName: resource.Name,
		Cwd:          cwd,
		EnvStrategy:  strategy,
		ResolvedCwd:  resolvedCwd,
		CUDA:         "unknown",
	}

	if _, stderr, err := e.exec(ctx, resource, "test -d "+shellQuote(resolvedCwd)); err != nil {
		return profile, fmt.Errorf("project cwd not accessible: %w%s", err, stderrSuffix(stderr))
	}

	if strategy == ProjectEnvAuto {
		e.detectAutoEnv(ctx, resource, profile, condaEnv)
	} else {
		e.detectRawEnv(ctx, resource, profile)
	}
	if profile.ResolvedEnv == "" {
		e.detectRawEnv(ctx, resource, profile)
		profile.Warnings = append(profile.Warnings, "project-env auto fell back to raw shell; python may not be project-local")
	}

	e.detectPythonCapabilities(ctx, resource, profile)
	profile.Entrypoints = e.detectEntrypoints(ctx, resource, profile.ResolvedCwd)
	profile.Metrics = e.detectMetricGlobs(ctx, resource, profile.ResolvedCwd)
	profile.Logs = e.detectLogGlobs(ctx, resource, profile.ResolvedCwd)
	return profile, nil
}

func (e *Executor) detectAutoEnv(ctx context.Context, resource *store.Resource, profile *store.ProjectProfile, condaEnv string) {
	cwd := shellQuote(profile.ResolvedCwd)
	if out, _, err := e.exec(ctx, resource, "cd "+cwd+" && if [ -x .venv/bin/python ]; then .venv/bin/python -c 'import sys; print(sys.executable)'; else exit 1; fi"); err == nil && strings.TrimSpace(out) != "" {
		profile.ResolvedEnv = ProjectEnvVenv
		profile.Python = strings.TrimSpace(out)
		profile.PythonOK = true
		profile.CommandPrefix = "source .venv/bin/activate"
		return
	}

	if out, _, err := e.exec(ctx, resource, "cd "+cwd+" && if command -v uv >/dev/null 2>&1; then uv run --no-sync python -c 'import sys; print(sys.executable)'; else exit 1; fi"); err == nil && strings.TrimSpace(out) != "" {
		profile.ResolvedEnv = ProjectEnvUV
		profile.Python = strings.TrimSpace(out)
		profile.PythonOK = true
		profile.CommandPrefix = "uv run"
		return
	}

	if condaEnv != "" {
		cmd := "cd " + cwd + " && " + condaActivationPrefix(resource, condaEnv) + "python -c 'import sys; print(sys.executable)'"
		if out, stderr, err := e.exec(ctx, resource, cmd); err == nil && strings.TrimSpace(out) != "" {
			profile.ResolvedEnv = ProjectEnvConda
			profile.EnvName = condaEnv
			profile.Python = strings.TrimSpace(out)
			profile.PythonOK = true
			profile.CommandPrefix = "conda activate " + condaEnv
		} else if strings.TrimSpace(stderr) != "" {
			profile.Warnings = append(profile.Warnings, "conda env unavailable: "+strings.TrimSpace(stderr))
		}
	}
}

func (e *Executor) detectRawEnv(ctx context.Context, resource *store.Resource, profile *store.ProjectProfile) {
	cmd := "cd " + shellQuote(profile.ResolvedCwd) + " && python -c 'import sys; print(sys.executable)'"
	profile.ResolvedEnv = ProjectEnvRaw
	profile.CommandPrefix = ""
	if out, _, err := e.exec(ctx, resource, cmd); err == nil && strings.TrimSpace(out) != "" {
		profile.Python = strings.TrimSpace(out)
		profile.PythonOK = true
	}
}

func (e *Executor) detectPythonCapabilities(ctx context.Context, resource *store.Resource, profile *store.ProjectProfile) {
	cmd := projectPythonCheckCommand(resource, profile, `try:
 import torch
 print("torch", "ok")
 print("cuda", torch.cuda.is_available(), torch.cuda.device_count())
except Exception as e:
 print("torch", "unavailable", e)`)
	out, stderr, err := e.exec(ctx, resource, cmd)
	text := strings.TrimSpace(firstNonEmpty(out, stderr))
	if err != nil {
		profile.Warnings = append(profile.Warnings, "python capability check failed: "+strings.TrimSpace(firstNonEmpty(stderr, err.Error())))
		return
	}
	profile.TorchOK = strings.Contains(text, "torch ok")
	if strings.Contains(text, "cuda True") {
		profile.CUDA = "ok"
		profile.CUDAOK = true
	} else if strings.Contains(text, "cuda False") {
		profile.CUDA = "unavailable"
	} else {
		profile.CUDA = "unknown"
	}
}

func (e *Executor) detectEntrypoints(ctx context.Context, resource *store.Resource, cwd string) []string {
	cmd := `cd ` + shellQuote(cwd) + ` && find . -maxdepth 3 -type f \( -name train.py -o -name main.py -o -name launcher.py \) 2>/dev/null | sort | head -20`
	out, _, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return nil
	}
	var entrypoints []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "./")
		if line == "" {
			continue
		}
		entrypoints = append(entrypoints, "python "+line)
	}
	return entrypoints
}

func (e *Executor) detectMetricGlobs(ctx context.Context, resource *store.Resource, cwd string) []string {
	cmd := `cd ` + shellQuote(cwd) + ` && { [ -d runs ] && echo 'runs/**/*.csv'; [ -d results ] && echo 'results/**/*.json'; [ -d outputs ] && echo 'outputs/**/*.json'; }`
	out, _, _ := e.exec(ctx, resource, cmd)
	return defaultedLines(out, []string{"runs/**/*.csv", "results/**/*.json"})
}

func (e *Executor) detectLogGlobs(ctx context.Context, resource *store.Resource, cwd string) []string {
	cmd := `cd ` + shellQuote(cwd) + ` && { [ -d logs ] && echo 'logs/**/*.log'; [ -d run_logs ] && echo 'run_logs/**/*.log'; }`
	out, _, _ := e.exec(ctx, resource, cmd)
	return defaultedLines(out, []string{"logs/**/*.log"})
}

func projectPythonCheckCommand(resource *store.Resource, profile *store.ProjectProfile, code string) string {
	lines := []string{"set -e", "cd " + shellQuote(profile.ResolvedCwd)}
	lines = append(lines, projectEnvPrelude(resource, profile)...)
	python := "python"
	if profile.ResolvedEnv == ProjectEnvUV {
		python = "uv run --no-sync python"
	}
	lines = append(lines, python+" - <<'PY'\n"+code+"\nPY")
	return strings.Join(lines, "\n")
}

func buildProjectWrappedCommand(resource *store.Resource, profile *store.ProjectProfile, command string) string {
	lines := []string{"set -e", "cd " + shellQuote(profile.ResolvedCwd)}
	lines = append(lines, projectEnvPrelude(resource, profile)...)
	if profile.ResolvedEnv == ProjectEnvUV {
		command = "uv run bash -lc " + shellQuote(command)
	}
	lines = append(lines, command)
	return strings.Join(lines, "\n")
}

func projectEnvPrelude(resource *store.Resource, profile *store.ProjectProfile) []string {
	switch profile.ResolvedEnv {
	case ProjectEnvVenv:
		return []string{"source .venv/bin/activate"}
	case ProjectEnvConda:
		lines := make([]string, 0, 4)
		for _, path := range condaInitCandidates(resource.CondaBase, resource.CondaInit) {
			lines = append(lines, fmt.Sprintf("if [ -f %s ]; then source %s; fi", shellPath(path), shellPath(path)))
		}
		lines = append(lines, `if ! command -v conda >/dev/null 2>&1; then echo "[aexp] conda not found; set resource conda_base/conda_init" >&2; exit 127; fi`)
		lines = append(lines, "conda activate "+shellQuote(profile.EnvName))
		return lines
	case ProjectEnvRaw:
		return []string{`echo "[aexp] project-env raw: using remote shell python" >&2`}
	default:
		return nil
	}
}

func resolveRemoteCwd(rootDir, cwd string) string {
	if cwd == "" {
		return rootDir
	}
	if strings.HasPrefix(cwd, "/") {
		return cwd
	}
	return strings.TrimRight(rootDir, "/") + "/" + cwd
}

func condaActivationPrefix(resource *store.Resource, env string) string {
	var lines []string
	for _, path := range condaInitCandidates(resource.CondaBase, resource.CondaInit) {
		lines = append(lines, fmt.Sprintf("if [ -f %s ]; then source %s; fi", shellPath(path), shellPath(path)))
	}
	lines = append(lines, "conda activate "+shellQuote(env))
	return strings.Join(lines, " && ") + " && "
}

func defaultedLines(out string, defaults []string) []string {
	seen := make(map[string]bool)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return defaults
	}
	return lines
}

func stderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
