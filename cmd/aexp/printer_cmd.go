package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	printerservice "github.com/ziwu/aexp/internal/printer"
	"github.com/ziwu/aexp/internal/store"
)

type printerCLIOptions struct {
	apiURL string
	token  string
}

func printerCmd() *cobra.Command {
	opts := &printerCLIOptions{}
	cmd := &cobra.Command{Use: "printer", Short: "Manage local experiment start/end receipts"}
	cmd.PersistentFlags().StringVar(&opts.apiURL, "api", defaultLocalAPIURL(), "Local aexp API base URL")
	cmd.PersistentFlags().StringVar(&opts.token, "token", "", "Local aexp API token (or AEXP_API_TOKEN)")
	cmd.AddCommand(printerStatusCmd(opts), printerEnableCmd(opts), printerDisableCmd(opts), printerTestCmd(opts), printerJobsCmd(opts), printerRetryCmd(opts))
	return cmd
}

func printerAPIRequest(ctx context.Context, opts *printerCLIOptions, method, path string, request, response any) error {
	var body io.Reader
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(opts.apiURL, "/")+path, body)
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token := opts.token
	if token == "" {
		token = strings.TrimSpace(os.Getenv("AEXP_API_TOKEN"))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("aexp serve is unavailable; printer commands never print directly: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error   string `json:"error"`
			Details string `json:"details"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Details != "" {
			return fmt.Errorf("printer API: %s", apiErr.Details)
		}
		return fmt.Errorf("printer API: HTTP %d %s", resp.StatusCode, apiErr.Error)
	}
	if response != nil {
		return json.NewDecoder(resp.Body).Decode(response)
	}
	return nil
}

func printerStatusCmd(opts *printerCLIOptions) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "status", Short: "Show printer and worker status", RunE: func(cmd *cobra.Command, args []string) error {
		var status printerservice.Status
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodGet, "/printer/status", nil, &status); err != nil {
			return err
		}
		if asJSON {
			return printJSON(status)
		}
		fmt.Printf("enabled: %t\nqueue: %s\navailable: %t (%s)\nqueued: %d\nfailed: %d\nuncertain: %d\n",
			status.Enabled, status.Queue, status.Available, status.QueueState, status.QueuedJobs, status.FailedJobs, status.UncertainJobs)
		if status.LastError != "" {
			fmt.Printf("last error: %s\n", status.LastError)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func printerEnableCmd(opts *printerCLIOptions) *cobra.Command {
	var queue string
	cmd := &cobra.Command{Use: "enable", Short: "Enable future automatic run receipts", RunE: func(cmd *cobra.Command, args []string) error {
		var settings store.PrinterSettings
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodPatch, "/printer/config", map[string]any{"enabled": true, "queue": queue}, &settings); err != nil {
			return err
		}
		fmt.Printf("Automatic experiment receipts enabled on %s (future events only).\n", settings.Queue)
		return nil
	}}
	cmd.Flags().StringVar(&queue, "queue", "Printer_POS_80", "CUPS queue name")
	return cmd
}

func printerDisableCmd(opts *printerCLIOptions) *cobra.Command {
	return &cobra.Command{Use: "disable", Short: "Disable future automatic run receipts", RunE: func(cmd *cobra.Command, args []string) error {
		var current printerservice.Status
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodGet, "/printer/status", nil, &current); err != nil {
			return err
		}
		var settings store.PrinterSettings
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodPatch, "/printer/config", map[string]any{"enabled": false, "queue": current.Queue}, &settings); err != nil {
			return err
		}
		fmt.Println("Automatic experiment receipts disabled.")
		return nil
	}}
}

func printerTestCmd(opts *printerCLIOptions) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "test", Short: "Queue one clearly labelled test receipt with cut", RunE: func(cmd *cobra.Command, args []string) error {
		var job store.PrintJob
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodPost, "/printer/test", struct{}{}, &job); err != nil {
			return err
		}
		if asJSON {
			return printJSON(job)
		}
		fmt.Printf("Test receipt queued: %s\n", job.ID)
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func printerJobsCmd(opts *printerCLIOptions) *cobra.Command {
	var limit int
	var asJSON bool
	cmd := &cobra.Command{Use: "jobs", Short: "List recent print jobs", RunE: func(cmd *cobra.Command, args []string) error {
		var result struct {
			Items []store.PrintJob `json:"items"`
			Total int              `json:"total"`
		}
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodGet, fmt.Sprintf("/printer/jobs?limit=%d", limit), nil, &result); err != nil {
			return err
		}
		if asJSON {
			return printJSON(result)
		}
		for _, job := range result.Items {
			fmt.Printf("%-28s %-7s %-10s %-24s %s\n", job.ID, job.Phase, job.State, job.RunID, job.CUPSJobID)
			if job.LastError != "" {
				fmt.Printf("  %s\n", job.LastError)
			}
		}
		return nil
	}}
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum jobs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func printerRetryCmd(opts *printerCLIOptions) *cobra.Command {
	return &cobra.Command{Use: "retry PRINT_JOB_ID", Short: "Retry a failed or uncertain print job", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var job store.PrintJob
		if err := printerAPIRequest(cmd.Context(), opts, http.MethodPost, "/printer/jobs/"+args[0]+"/retry", struct{}{}, &job); err != nil {
			return err
		}
		fmt.Printf("Print job queued again: %s\n", job.ID)
		return nil
	}}
}
