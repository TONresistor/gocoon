package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWrapShortAnswerByteExact(t *testing.T) {
	got := wrapShortAnswer("Request sent")
	want := "<!DOCTYPE html>\n<html><body>\nRequest sent<br/>\n<a href=\"/stats\">return to stats</a>\n</html></body>\n"
	if got != want {
		t.Errorf("wrapShortAnswer mismatch:\nwant %q\ngot  %q", want, got)
	}
}

func TestJSONStatsShape(t *testing.T) {
	state := &RunnerState{
		Enabled:          true,
		GitCommit:        "abcdef",
		RootAddress:      MainnetRoot,
		OwnerAddress:     MainnetRoot,
		CheckImageHashes: false,
		WalletBalance:    1_500_000_000,
		TONLastSyncedAt:  1_700_000_000,
	}
	cp := NewControlPlane(0, nil, state, slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/jsonstats", nil)
	cp.handleJSONStats(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: %s", ct)
	}
	var got jsonStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Status.WalletBalance != 1_500_000_000 {
		t.Errorf("wallet_balance: %d", got.Status.WalletBalance)
	}
	if got.Status.GitCommit != "abcdef" {
		t.Errorf("git_commit: %s", got.Status.GitCommit)
	}
	if got.LocalConf.RootAddress != MainnetRoot {
		t.Errorf("root: %s", got.LocalConf.RootAddress)
	}
	// arrays must be present (not nil), even if empty.
	if got.ProxyConnections == nil {
		t.Errorf("proxy_connections must be []")
	}
	if got.Proxies == nil {
		t.Errorf("proxies must be []")
	}
}

func TestRequestEndpointMissingProxyParam(t *testing.T) {
	state := &RunnerState{}
	cp := NewControlPlane(0, nil, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := cp.requestHandler("close")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request/close", nil)
	h(rec, req)

	if rec.Code != 200 {
		t.Errorf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "missing proxy") {
		t.Errorf("body: %q", body)
	}
}

func TestRequestEndpointInvalidProxyAddr(t *testing.T) {
	state := &RunnerState{}
	cp := NewControlPlane(0, nil, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := cp.requestHandler("close")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request/close?proxy=EQUnknown", nil)
	h(rec, req)

	if !strings.Contains(strings.ToLower(rec.Body.String()), "invalid proxy address") {
		t.Errorf("body: %q", rec.Body.String())
	}
}

func TestRequestEndpointUninitialized(t *testing.T) {
	state := &RunnerState{}
	cp := NewControlPlane(0, nil, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Broadcaster nil and CocoonWalletAddr nil → "client not initialized".
	h := cp.requestHandler("close")
	const validProxy = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request/close?proxy="+validProxy, nil)
	h(rec, req)

	if !strings.Contains(strings.ToLower(rec.Body.String()), "client not initialized") {
		t.Errorf("body: %q", rec.Body.String())
	}
}

func TestControlPlaneStartShutdown(t *testing.T) {
	state := &RunnerState{}
	cp := NewControlPlane(0, nil, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := cp.Start(); err != nil {
		t.Fatal(err)
	}
	// Double start should fail.
	if err := cp.Start(); err == nil {
		t.Errorf("double start should fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cp.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
	// Idempotent shutdown.
	if err := cp.Shutdown(ctx); err != nil {
		t.Errorf("double shutdown: %v", err)
	}
}
