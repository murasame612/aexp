package printer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type runnerCall struct {
	name  string
	args  []string
	stdin string
}

type fakeRunner struct {
	call runnerCall
	out  string
	err  error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdin string) (string, error) {
	f.call = runnerCall{name: name, args: append([]string(nil), args...), stdin: stdin}
	return f.out, f.err
}

func TestCUPSPrintUsesPOS80CutOptionsAndParsesJobID(t *testing.T) {
	runner := &fakeRunner{out: "request id is Printer_POS_80-42 (1 file(s))"}
	cups := &CUPS{runner: runner}
	jobID, err := cups.Print(t.Context(), "Printer_POS_80", "AEXP RUN", "receipt\n")
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "Printer_POS_80-42" {
		t.Fatalf("job id = %q", jobID)
	}
	if runner.call.name != "/usr/bin/lp" {
		t.Fatalf("unexpected command: %#v", runner.call)
	}
	args := strings.Join(runner.call.args, "\x00")
	for _, required := range []string{
		"media=Custom.72x",
		"PageCutType=1PartialCutPage",
		"DocCutType=0NoCutDoc",
		"FeedCutAfterJobEnd=0None",
		"document-format=application/vnd.cups-raster",
		"job-sheets=none",
		"PrintDensity=6",
		"HalftoneType=None",
	} {
		if !strings.Contains(args, required) {
			t.Fatalf("print args missing %q: %#v", required, runner.call.args)
		}
	}
	if strings.Contains(args, "media=Custom.72x80mm") || strings.Contains(args, "DocCutType=1PartialCutDoc") {
		t.Fatalf("fixed paper or duplicate document cut survived: %#v", runner.call.args)
	}
	if cuts := strings.Count(args, "1PartialCut"); cuts != 1 {
		t.Fatalf("expected exactly one physical cut option, got %d: %#v", cuts, runner.call.args)
	}
	syncWord := runner.call.stdin[:min(4, len(runner.call.stdin))]
	if syncWord != "RaS2" && syncWord != "2SaR" && syncWord != "RaS3" && syncWord != "3SaR" {
		t.Fatalf("CUPS payload is not native raster: % x", []byte(runner.call.stdin[:min(8, len(runner.call.stdin))]))
	}
}

func TestCUPSPrintReportsSubmissionFailure(t *testing.T) {
	runner := &fakeRunner{out: "printer offline", err: errors.New("exit status 1")}
	_, err := (&CUPS{runner: runner}).Print(t.Context(), "q", "t", "body")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCUPSPrintParsesLocalizedMacOSOutput(t *testing.T) {
	runner := &fakeRunner{out: "请求id是Printer_POS_80-21（0个文件）"}
	jobID, err := (&CUPS{runner: runner}).Print(t.Context(), "Printer_POS_80", "test", "body")
	if err != nil || jobID != "Printer_POS_80-21" {
		t.Fatalf("job id = %q, err = %v", jobID, err)
	}
}
