package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziwu/aexp/internal/executor"
)

func TestExecViaLocalAPISendsRequest(t *testing.T) {
	var got executor.ExecRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/exec" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(executor.ExecResult{
			Stdout:   "ok\n",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	result, usedAPI, err := execViaLocalAPI(t.Context(), srv.URL+"/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		Actor:      "cli",
		TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("execViaLocalAPI error: %v", err)
	}
	if !usedAPI {
		t.Fatal("expected API path to be used")
	}
	if result == nil || result.Stdout != "ok\n" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got.Actor != "cli" {
		t.Fatalf("actor = %q, want cli", got.Actor)
	}
}

func TestExecViaLocalAPIFallsBackOnUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	result, usedAPI, err := execViaLocalAPI(t.Context(), srv.URL+"/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("execViaLocalAPI error: %v", err)
	}
	if usedAPI {
		t.Fatal("expected unauthorized local API to fall back")
	}
	if result != nil {
		t.Fatalf("expected nil result on fallback, got %#v", result)
	}
}

func TestExecViaLocalAPIFallsBackWhenUnavailable(t *testing.T) {
	result, usedAPI, err := execViaLocalAPI(t.Context(), "http://127.0.0.1:1/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("execViaLocalAPI error: %v", err)
	}
	if usedAPI {
		t.Fatal("expected unreachable local API to fall back")
	}
	if result != nil {
		t.Fatalf("expected nil result on fallback, got %#v", result)
	}
}

func TestExecViaLocalAPIReturnsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "EXEC_FAILED",
			"details": "remote command failed",
		})
	}))
	defer srv.Close()

	result, usedAPI, err := execViaLocalAPI(t.Context(), srv.URL+"/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		TimeoutSec: 1,
	})
	if err == nil || err.Error() != "local aexp API exec failed: remote command failed" {
		t.Fatalf("execViaLocalAPI error = %v", err)
	}
	if !usedAPI {
		t.Fatal("expected API business error to stay on API path")
	}
	if result != nil {
		t.Fatalf("expected nil result on API error, got %#v", result)
	}
}
