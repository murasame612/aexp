package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/store"
)

const maxPollBackoff = 2 * time.Minute

// Manager polls resource health on a fixed interval.
type Manager struct {
	store    store.Store
	pool     *executor.SSHPool
	interval time.Duration
	logger   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	resources map[string]pollState // resource_id -> polling state
}

type pollState struct {
	cancel    context.CancelFunc
	signature string
}

// NewManager creates a new monitor manager.
func NewManager(store store.Store, pool *executor.SSHPool, interval time.Duration, logger *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:     store,
		pool:      pool,
		interval:  interval,
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
		resources: make(map[string]pollState),
	}
}

// Start begins polling all registered resources and watches for new ones.
func (m *Manager) Start() error {
	resources, err := m.store.ListResources(m.ctx)
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	for i := range resources {
		m.startPolling(&resources[i])
	}

	// Background sync: detect resources added/removed via CLI or API
	m.wg.Add(1)
	go m.syncLoop()

	return nil
}

// Stop gracefully shuts down all polling goroutines.
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()
}

// AddResource starts polling a new resource.
func (m *Manager) AddResource(r *store.Resource) {
	m.startPolling(r)
}

// RemoveResource stops polling a resource.
func (m *Manager) RemoveResource(resourceID string) {
	m.mu.Lock()
	if state, ok := m.resources[resourceID]; ok {
		state.cancel()
		delete(m.resources, resourceID)
	}
	m.mu.Unlock()
}

func (m *Manager) startPolling(r *store.Resource) {
	m.mu.Lock()
	defer m.mu.Unlock()

	signature := resourcePollSignature(r)
	// Don't start if already polling the same resource config.
	if state, ok := m.resources[r.ID]; ok && state.signature == signature {
		return
	} else if ok {
		state.cancel()
		delete(m.resources, r.ID)
	}

	ctx, cancel := context.WithCancel(m.ctx)
	m.resources[r.ID] = pollState{cancel: cancel, signature: signature}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.pollLoop(ctx, r)
	}()
}

// syncLoop periodically checks DB for new/removed resources.
func (m *Manager) syncLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.syncResources()
		}
	}
}

func (m *Manager) syncResources() {
	resources, err := m.store.ListResources(m.ctx)
	if err != nil {
		m.logger.Warn("sync resources: list failed", "error", err)
		return
	}

	// Build set of current DB resource IDs
	current := make(map[string]bool, len(resources))
	for i := range resources {
		current[resources[i].ID] = true
		m.startPolling(&resources[i])
	}

	// Stop polling resources that no longer exist in DB
	m.mu.Lock()
	for id, state := range m.resources {
		if !current[id] {
			state.cancel()
			delete(m.resources, id)
		}
	}
	m.mu.Unlock()
}

func resourcePollSignature(r *store.Resource) string {
	return strings.Join([]string{
		r.Host,
		strconv.Itoa(r.Port),
		r.User,
		r.AuthRef,
		r.SocksProxy,
		r.ProxyCommand,
		r.RootDir,
	}, "\x00")
}

func (m *Manager) pollLoop(ctx context.Context, r *store.Resource) {
	failures := 0

	for {
		ok := m.poll(ctx, r)
		if ctx.Err() != nil {
			return
		}
		if ok {
			failures = 0
		} else {
			failures++
		}
		delay := pollBackoffDelay(m.interval, failures)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func pollBackoffDelay(base time.Duration, failures int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if failures <= 0 {
		return base
	}
	shift := failures - 1
	if shift > 30 {
		shift = 30
	}
	multiplier := math.Pow(2, float64(shift))
	delay := time.Duration(float64(base) * multiplier)
	if delay <= 0 || delay > maxPollBackoff {
		return maxPollBackoff
	}
	return delay
}

func (m *Manager) poll(ctx context.Context, r *store.Resource) bool {
	snap, err := m.PollResource(ctx, r)
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		m.logger.Warn("poll failed", "resource", r.Name, "error", err)
		m.pool.RemoveByHost(r.Host, r.Port)
		if r.Status != store.ResourceStatusUnreachable {
			r.Status = store.ResourceStatusUnreachable
			m.store.UpdateResource(ctx, r)
		}
		return false
	}

	// Save snapshot
	if err := m.store.SaveSnapshot(ctx, snap); err != nil {
		m.logger.Error("save snapshot", "error", err)
		return true
	}

	// Derive resource status
	newStatus := deriveStatus(snap)
	if newStatus != r.Status {
		r.Status = newStatus
		m.store.UpdateResource(ctx, r)
	}
	return true
}

// PollResource executes the probe script and parses the result.
func (m *Manager) PollResource(ctx context.Context, r *store.Resource) (*store.Snapshot, error) {
	probeScript := buildProbeScript(r.RootDir)
	stdout, stderr, err := m.pool.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, probeScript, r.SocksProxy, r.ProxyCommand)
	if err != nil {
		return nil, fmt.Errorf("exec probe: %w (stderr: %s)", err, stderr)
	}

	snap := &store.Snapshot{
		ResourceID: r.ID,
	}

	// Try to find current run ID
	runs, _ := m.store.ListRuns(ctx, store.RunFilter{ResourceID: r.ID, Status: store.RunStatusRunning})
	if len(runs) > 0 {
		snap.RunID = runs[0].ID
	}

	parseProbeOutput(stdout, snap)
	return snap, nil
}

func buildProbeScript(rootDir string) string {
	return fmt.Sprintf(`ROOT_DIR=%q
OS_NAME="$(uname -s 2>/dev/null || echo unknown)"
echo "---CPU---"
if [ "$OS_NAME" = "Darwin" ]; then
  top -l 1 -n 0 2>/dev/null | awk '/CPU usage/ {u=$3; s=$5; gsub("%%","",u); gsub("%%","",s); printf "cpu_percent|%%.1f\n", u+s}'
else
  top -bn1 | grep '%%Cpu' | head -1
fi
echo "---MEM---"
if [ "$OS_NAME" = "Darwin" ]; then
  total_bytes="$(sysctl -n hw.memsize 2>/dev/null || echo 0)"
  total_mb=$(( total_bytes / 1024 / 1024 ))
  free_pct="$(memory_pressure -Q 2>/dev/null | awk -F': ' '/System-wide memory free percentage/ {gsub("%%","",$2); print $2}')"
  if [ -n "$free_pct" ]; then
    used_mb=$(( total_mb * (100 - free_pct) / 100 ))
  else
    page_size="$(sysctl -n hw.pagesize 2>/dev/null || echo 4096)"
    free_pages="$(vm_stat 2>/dev/null | awk '/Pages free/ {gsub("\\.","",$3); print $3}')"
    speculative_pages="$(vm_stat 2>/dev/null | awk '/Pages speculative/ {gsub("\\.","",$3); print $3}')"
    purgeable_pages="$(vm_stat 2>/dev/null | awk '/Pages purgeable/ {gsub("\\.","",$3); print $3}')"
    free_pages="${free_pages:-0}"
    speculative_pages="${speculative_pages:-0}"
    purgeable_pages="${purgeable_pages:-0}"
    reclaimable_bytes=$(( (free_pages + speculative_pages + purgeable_pages) * page_size ))
    used_mb=$(( (total_bytes - reclaimable_bytes) / 1024 / 1024 ))
  fi
  echo "mem_mb|$used_mb|$total_mb"
else
  grep -E 'MemTotal|MemAvailable' /proc/meminfo
fi
echo "---LOAD---"
if [ "$OS_NAME" = "Darwin" ]; then
  sysctl -n vm.loadavg 2>/dev/null | awk '{print $2, $3, $4}'
else
  cat /proc/loadavg
fi
echo "---GPU---"
if [ "$OS_NAME" = "Darwin" ]; then
  echo "no-gpu"
else
  nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader,nounits 2>/dev/null || echo "no-gpu"
fi
echo "---DISK---"
df -m "$ROOT_DIR" 2>/dev/null | tail -1`, rootDir)
}

func parseProbeOutput(output string, snap *store.Snapshot) {
	sections := strings.Split(output, "---")

	// Skip leading empty element from split (before first ---)
	start := 0
	if len(sections) > 0 && strings.TrimSpace(sections[0]) == "" {
		start = 1
	}

	for i := start; i < len(sections)-1; i += 2 {
		tag := strings.TrimSpace(sections[i])
		content := ""
		if i+1 < len(sections) {
			content = sections[i+1]
		}

		switch tag {
		case "CPU":
			snap.CPUPercent = parseCPU(content)
		case "MEM":
			snap.MemUsedMB, snap.MemTotalMB = parseMem(content)
		case "LOAD":
			snap.Load1m, snap.Load5m, snap.Load15m = parseLoad(content)
		case "GPU":
			snap.GPUJSON = parseGPU(content)
		case "DISK":
			snap.DiskJSON = parseDisk(content)
		}
	}
}

func parseCPU(line string) float64 {
	// top output: %Cpu(s): 23.1 us,  1.2 sy,  0.0 ni, 75.3 id,  0.3 wa, ...
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "cpu_percent|") {
		v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "cpu_percent|")), 64)
		return v
	}
	parts := strings.Split(line, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasSuffix(p, " id") {
			p = strings.TrimSuffix(p, " id")
			p = strings.TrimSpace(p)
			if idle, err := strconv.ParseFloat(p, 64); err == nil {
				return 100.0 - idle
			}
		}
	}
	return 0
}

func parseMem(content string) (usedMB, totalMB float64) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "mem_mb|") {
		parts := strings.Split(content, "|")
		if len(parts) >= 3 {
			usedMB, _ = strconv.ParseFloat(parts[1], 64)
			totalMB, _ = strconv.ParseFloat(parts[2], 64)
		}
		return
	}
	lines := strings.Split(content, "\n")
	var total, available float64
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseKb(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			available = parseKb(line)
		}
	}
	totalMB = total / 1024
	usedMB = (total - available) / 1024
	return
}

func parseKb(line string) float64 {
	// MemTotal:       65536000 kB
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		if v, err := strconv.ParseFloat(parts[1], 64); err == nil {
			return v
		}
	}
	return 0
}

func parseLoad(content string) (m1, m5, m15 float64) {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) >= 3 {
		m1, _ = strconv.ParseFloat(fields[0], 64)
		m5, _ = strconv.ParseFloat(fields[1], 64)
		m15, _ = strconv.ParseFloat(fields[2], 64)
	}
	return
}

func parseGPU(content string) string {
	content = strings.TrimSpace(content)
	if content == "no-gpu" || content == "" {
		return "[]"
	}

	type gpuInfo struct {
		Index    int     `json:"index"`
		Name     string  `json:"name"`
		Util     float64 `json:"util"`
		MemUsed  float64 `json:"mem_used"`
		MemTotal float64 `json:"mem_total"`
	}

	var gpus []gpuInfo
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 5)
		if len(parts) < 5 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		util, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		memUsed, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
		memTotal, _ := strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)

		gpus = append(gpus, gpuInfo{
			Index:    idx,
			Name:     strings.TrimSpace(parts[1]),
			Util:     util,
			MemUsed:  memUsed,
			MemTotal: memTotal,
		})
	}

	if len(gpus) == 0 {
		return "[]"
	}

	// Simple JSON encoding without importing encoding/json
	var sb strings.Builder
	sb.WriteString("[")
	for i, g := range gpus {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"index":%d,"name":"%s","util":%.1f,"mem_used":%.0f,"mem_total":%.0f}`,
			g.Index, g.Name, g.Util, g.MemUsed, g.MemTotal)
	}
	sb.WriteString("]")
	return sb.String()
}

func parseDisk(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "{}"
	}
	// df output: /dev/sda1        500G   120G   380G  24% /workspace
	fields := strings.Fields(content)
	if len(fields) >= 4 {
		return fmt.Sprintf(`{"total":"%s","used":"%s","avail":"%s","use_pct":"%s"}`,
			fields[1], fields[2], fields[3], fields[4])
	}
	return "{}"
}

func deriveStatus(snap *store.Snapshot) string {
	// Check GPU utilization
	if snap.GPUJSON != "[]" && snap.GPUJSON != "" {
		// Quick check: if any GPU has util > 5%, it's busy
		if strings.Contains(snap.GPUJSON, `"util"`) {
			// Simple heuristic: if total util > 0, busy
			// (proper parsing would use encoding/json but we keep it simple)
			for _, part := range strings.Split(snap.GPUJSON, `"util":`) {
				if len(part) > 5 {
					val := ""
					for _, c := range part[1:] {
						if c >= '0' && c <= '9' || c == '.' {
							val += string(c)
						} else {
							break
						}
					}
					if v, _ := strconv.ParseFloat(val, 64); v > 5 {
						return store.ResourceStatusBusy
					}
				}
			}
		}
	}

	return store.ResourceStatusIdle
}
