package printer

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Device is the small seam between lifecycle policy and the local CUPS tools.
type Device interface {
	Print(ctx context.Context, queue, title, receipt string) (string, error)
	Status(ctx context.Context, queue string) (QueueStatus, error)
}

type QueueStatus struct {
	Available bool   `json:"available"`
	State     string `json:"queue_state"`
	Detail    string `json:"detail,omitempty"`
}

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin string) (string, error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type CUPS struct{ runner commandRunner }

func NewCUPS() *CUPS { return &CUPS{runner: osCommandRunner{}} }

var cupsJobPattern = regexp.MustCompile(`(?i)request id is ([^\s]+)`)
var cupsJobIDFallbackPattern = regexp.MustCompile(`([A-Za-z0-9_.-]+-[0-9]+)`)

func (c *CUPS) Print(ctx context.Context, queue, title, receipt string) (string, error) {
	payload, document, err := renderReceiptCUPSRaster(receipt)
	if err != nil {
		return "", fmt.Errorf("render receipt: %w", err)
	}
	args := []string{
		"-d", queue,
		"-t", title,
		"-o", "media=" + document.Media,
		"-o", "PageCutType=1PartialCutPage",
		"-o", "DocCutType=0NoCutDoc",
		"-o", "FeedCutAfterJobEnd=0None",
		"-o", "job-sheets=none",
		"-o", "PrintDensity=6",
		"-o", "HalftoneType=None",
		"-o", "document-format=" + document.ContentType,
	}
	out, err := c.runner.Run(ctx, "/usr/bin/lp", args, string(payload))
	if err != nil {
		return "", fmt.Errorf("CUPS submission failed: %w: %s", err, out)
	}
	match := cupsJobPattern.FindStringSubmatch(out)
	if len(match) == 2 {
		return strings.TrimRight(match[1], "（("), nil
	}
	// macOS localizes lp output (for example "请求id是Queue-21（0个文件）").
	// The CUPS queue-job token itself is stable across locales.
	match = cupsJobIDFallbackPattern.FindStringSubmatch(out)
	if len(match) == 2 {
		return match[1], nil
	}
	return "", fmt.Errorf("CUPS accepted output could not be parsed: %s", out)
}

func (c *CUPS) Status(ctx context.Context, queue string) (QueueStatus, error) {
	out, err := c.runner.Run(ctx, "/usr/bin/lpstat", []string{"-p", queue}, "")
	if err != nil {
		return QueueStatus{Available: false, State: "unavailable", Detail: out}, nil
	}
	state := "idle"
	lower := strings.ToLower(out)
	if strings.Contains(lower, "disabled") {
		state = "disabled"
	} else if strings.Contains(lower, "printing") {
		state = "printing"
	}
	return QueueStatus{Available: true, State: state, Detail: out}, nil
}
