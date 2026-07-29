package printer

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ziwu/aexp/internal/store"
)

const receiptWidth = 32

func cleanReceiptText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func truncateReceiptText(value string, width int) string {
	value = cleanReceiptText(value)
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func cleanReceiptValue(value string) string {
	return truncateReceiptText(value, receiptWidth)
}

func receiptLine(label, value string) string {
	prefix := fmt.Sprintf("%-10s ", label+":")
	return prefix + truncateReceiptText(value, receiptWidth-len([]rune(prefix)))
}

func formatReceiptTime(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02 15:04:05")
}

func formatDuration(start, finish *time.Time) string {
	if start == nil || finish == nil || finish.Before(*start) {
		return "unknown"
	}
	d := finish.Sub(*start).Round(time.Second)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func BuildReceipt(run *store.Run, resourceName, phase string, event store.PrinterRunEvent) (title, body string) {
	name := run.Name
	if strings.TrimSpace(name) == "" {
		name = run.ID
	}
	heading := "AEXP RUN STARTED"
	if phase == store.PrintPhaseEnd {
		heading = "AEXP RUN " + strings.ToUpper(event.Status)
	}
	title = cleanReceiptValue(heading + " " + run.ID)
	lines := []string{strings.Repeat("=", receiptWidth), heading, strings.Repeat("=", receiptWidth)}
	if event.Kind == store.RunKindSmoke {
		lines = append(lines, "PLUMBING ONLY - NOT A RESULT", strings.Repeat("-", receiptWidth))
	}
	lines = append(lines,
		receiptLine("run", run.ID),
		receiptLine("name", name),
		receiptLine("kind", event.Kind),
		receiptLine("evidence", run.EvidenceGrade),
		receiptLine("resource", resourceName),
		receiptLine("gpu", fmt.Sprint(run.GPUIndex)),
	)
	if phase == store.PrintPhaseStart {
		if event.StartedAt != nil {
			lines = append(lines, receiptLine("started", formatReceiptTime(*event.StartedAt)))
		}
		lines = append(lines, buildStartDetailLines(run)...)
	} else {
		if event.StartedAt == nil {
			lines = append(lines, "NEVER ENTERED RUNNING")
		}
		lines = append(lines, receiptLine("duration", formatDuration(event.StartedAt, event.FinishedAt)))
		if run.ExitCode.Valid {
			lines = append(lines, receiptLine("exit", fmt.Sprint(run.ExitCode.Int64)))
		}
		lines = append(lines, receiptLine("data", run.DataFinalizationState))
		if event.FinishedAt != nil {
			lines = append(lines, receiptLine("finished", formatReceiptTime(*event.FinishedAt)))
		}
	}
	lines = append(lines, strings.Repeat("=", receiptWidth), "")
	return title, strings.Join(lines, "\n")
}

func BuildTestReceipt(now time.Time) (string, string) {
	title := "AEXP PRINTER TEST"
	lines := []string{
		strings.Repeat("=", receiptWidth),
		"AEXP PRINTER TEST",
		"TEST RECEIPT - NOT AN EXPERIMENT",
		strings.Repeat("=", receiptWidth),
		receiptLine("time", formatReceiptTime(now)),
		"CUPS + FEED + CUT CHECK",
		strings.Repeat("=", receiptWidth),
		"",
	}
	return title, strings.Join(lines, "\n")
}
