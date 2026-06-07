package explore

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ziwu/aexp/internal/executor"
)

// Discovery contains the discovered environment info of a remote host.
type Discovery struct {
	Host       string      `json:"host"`
	OS         string      `json:"os"`
	Arch       string      `json:"arch"`
	GPUs       []GPUInfo   `json:"gpus"`
	CondaEnvs  []CondaEnv  `json:"conda_envs"`
	Pythons    []PythonBin `json:"pythons"`
	Workspaces []Workspace `json:"workspaces"`
	TmuxCount  int         `json:"tmux_sessions"`
	AexpRuns   int         `json:"aexp_runs"`
}

type GPUInfo struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	MemTotal string `json:"mem_total"`
}

type CondaEnv struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type PythonBin struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Env     string `json:"env,omitempty"`
}

type Workspace struct {
	Path       string `json:"path"`
	SubdirCount int   `json:"subdir_count"`
}

// Explore runs discovery probes on a remote host and returns the results.
func Explore(ctx context.Context, pool *executor.SSHPool, host string, port int, user string, keyPath string) (*Discovery, error) {
	d := &Discovery{Host: host}

	// Single big probe script to minimize SSH roundtrips
	script := buildProbeScript()

	stdout, stderr, err := pool.Exec(ctx, host, port, user, keyPath, script)
	if err != nil {
		return nil, fmt.Errorf("ssh exec failed: %w (stderr: %s)", err, stderr)
	}

	parseProbeOutput(stdout, d)
	return d, nil
}

func buildProbeScript() string {
	return `#!/bin/bash
echo "---OS---"
uname -s -m
cat /etc/os-release 2>/dev/null | grep ^PRETTY_NAME | head -1

echo "---GPU---"
nvidia-smi --query-gpu=index,name,memory.total --format=csv,noheader,nounits 2>/dev/null || echo "no-gpu"

echo "---CONDA---"
# Try common conda locations
for conda_bin in conda /opt/conda/bin/conda ~/miniconda3/bin/conda ~/anaconda3/bin/conda; do
  if command -v "$conda_bin" &>/dev/null || [ -x "$conda_bin" ]; then
    CONDA_EXE="$conda_bin"
    break
  fi
done
if [ -n "$CONDA_EXE" ]; then
  source "$("$CONDA_EXE" info --base 2>/dev/null)/etc/profile.d/conda.sh" 2>/dev/null
  conda env list 2>/dev/null | grep -v '^#' | grep -v '^$'
else
  echo "no-conda"
fi

echo "---PYTHON---"
for p in /usr/bin/python3 /usr/bin/python /opt/conda/bin/python; do
  if [ -x "$p" ]; then
    ver=$("$p" --version 2>&1)
    echo "$p|$ver"
  fi
done
# Also check conda env pythons
if [ -n "$CONDA_EXE" ]; then
  for env_dir in /opt/conda/envs/*/bin/python ~/miniconda3/envs/*/bin/python ~/anaconda3/envs/*/bin/python; do
    if [ -x "$env_dir" ]; then
      ver=$("$env_dir" --version 2>&1)
      env_name=$(echo "$env_dir" | sed 's|.*/envs/||' | sed 's|/bin/python||')
      echo "$env_dir|$ver|env:$env_name"
    fi
  done
fi

echo "---WORKSPACE---"
for dir in /workspace /home/*/workspace /data /mnt/workspace; do
  if [ -d "$dir" ]; then
    count=$(find "$dir" -maxdepth 1 -type d 2>/dev/null | wc -l)
    echo "$dir|$count"
  fi
done

echo "---TMUX---"
tmux list-sessions 2>/dev/null | wc -l || echo "0"

echo "---AEXP---"
# Check for existing aexp runs
count=0
for dir in /workspace/.aexp/runs/*/; do
  if [ -d "$dir" ] && [ ! -f "$dir/exit_code" ]; then
    count=$((count + 1))
  fi
done
echo "$count"
`
}

func parseProbeOutput(output string, d *Discovery) {
	sections := strings.Split(output, "---")

	for i := 0; i < len(sections)-1; i += 2 {
		tag := strings.TrimSpace(sections[i])
		content := ""
		if i+1 < len(sections) {
			content = sections[i+1]
		}

		switch tag {
		case "OS":
			parseOS(content, d)
		case "GPU":
			parseGPU(content, d)
		case "CONDA":
			parseConda(content, d)
		case "PYTHON":
			parsePython(content, d)
		case "WORKSPACE":
			parseWorkspace(content, d)
		case "TMUX":
			parseTmux(content, d)
		case "AEXP":
			parseAexpRuns(content, d)
		}
	}
}

func parseOS(content string, d *Discovery) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			d.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
		} else if strings.Contains(line, " ") {
			// uname -s -m output: "Darwin arm64" or "Linux x86_64"
			d.Arch = line
		}
	}
	if d.OS == "" {
		d.OS = d.Arch
	}
}

func parseGPU(content string, d *Discovery) {
	content = strings.TrimSpace(content)
	if content == "no-gpu" || content == "" {
		return
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 3 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		name := strings.TrimSpace(parts[1])
		mem := strings.TrimSpace(parts[2])
		d.GPUs = append(d.GPUs, GPUInfo{Index: idx, Name: name, MemTotal: mem + " MB"})
	}
}

func parseConda(content string, d *Discovery) {
	content = strings.TrimSpace(content)
	if content == "no-conda" || content == "" {
		return
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		// conda env list format: env_name    /path/to/env
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name := fields[0]
			path := fields[len(fields)-1]
			// Skip the header-like lines
			if name == "Name" || name == "base" && !strings.Contains(path, "/") {
				if name == "base" && len(fields) >= 2 {
					path = fields[len(fields)-1]
				} else {
					continue
				}
			}
			d.CondaEnvs = append(d.CondaEnvs, CondaEnv{Name: name, Path: path})
		}
	}
}

func parsePython(content string, d *Discovery) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		bin := PythonBin{
			Path:    parts[0],
			Version: strings.TrimSpace(parts[1]),
		}
		if len(parts) >= 3 {
			bin.Env = parts[2]
		}
		d.Pythons = append(d.Pythons, bin)
	}
}

func parseWorkspace(content string, d *Discovery) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) < 2 {
			continue
		}
		count, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		d.Workspaces = append(d.Workspaces, Workspace{
			Path:        parts[0],
			SubdirCount: count - 1, // subtract the dir itself
		})
	}
}

func parseTmux(content string, d *Discovery) {
	content = strings.TrimSpace(content)
	d.TmuxCount, _ = strconv.Atoi(content)
}

func parseAexpRuns(content string, d *Discovery) {
	content = strings.TrimSpace(content)
	d.AexpRuns, _ = strconv.Atoi(content)
}

// FormatDiscovery returns a human-readable summary of the discovery.
func FormatDiscovery(d *Discovery) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Host:       %s\n", d.Host))
	b.WriteString(fmt.Sprintf("OS:         %s\n", d.OS))

	if len(d.GPUs) > 0 {
		b.WriteString(fmt.Sprintf("GPU:        %d device(s)\n", len(d.GPUs)))
		for _, g := range d.GPUs {
			b.WriteString(fmt.Sprintf("  [%d] %s (%s)\n", g.Index, g.Name, g.MemTotal))
		}
	} else {
		b.WriteString("GPU:        none detected\n")
	}

	if len(d.CondaEnvs) > 0 {
		b.WriteString("Conda envs:\n")
		for _, e := range d.CondaEnvs {
			b.WriteString(fmt.Sprintf("  - %-20s %s\n", e.Name, e.Path))
		}
	} else {
		b.WriteString("Conda:      not found\n")
	}

	if len(d.Pythons) > 0 {
		b.WriteString("Python:\n")
		for _, p := range d.Pythons {
			envTag := ""
			if p.Env != "" {
				envTag = " (" + p.Env + ")"
			}
			b.WriteString(fmt.Sprintf("  - %-50s %s%s\n", p.Path, p.Version, envTag))
		}
	}

	if len(d.Workspaces) > 0 {
		b.WriteString("Workspaces:\n")
		for _, w := range d.Workspaces {
			b.WriteString(fmt.Sprintf("  - %-30s (%d subdirs)\n", w.Path, w.SubdirCount))
		}
	}

	b.WriteString(fmt.Sprintf("tmux:       %d session(s)\n", d.TmuxCount))
	b.WriteString(fmt.Sprintf("aexp runs:  %d active\n", d.AexpRuns))

	return b.String()
}
