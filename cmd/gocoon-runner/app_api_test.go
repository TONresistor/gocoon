package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestAppAPI(t *testing.T) *AppAPI {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := &RunnerState{}
	engine := NewEngine(dir, state, logger)
	return NewAppAPI(dir, 10999, engine, state, newLogRing(10), logger)
}

func serveAppAPI(a *AppAPI) *http.ServeMux {
	mux := http.NewServeMux()
	a.routes(mux, func(http.ResponseWriter) {})
	return mux
}

func TestAppStateBeforeOnboarding(t *testing.T) {
	mux := serveAppAPI(newTestAppAPI(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var st appState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.HasWallet || st.HasConfig || st.Engine.Running {
		t.Fatalf("expected pristine state, got %+v", st)
	}
}

func TestAppWalletCreateThenState(t *testing.T) {
	a := newTestAppAPI(t)
	mux := serveAppAPI(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/create", nil))
	if rec.Code != 200 {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Backup *struct {
			OwnerMnemonic []string `json:"owner_mnemonic"`
			FundAddress   string   `json:"fund_address"`
		} `json:"backup"`
		State appState `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Backup == nil || len(created.Backup.OwnerMnemonic) != 24 {
		t.Fatalf("backup missing or malformed: %s", rec.Body)
	}
	if !created.State.HasWallet || !created.State.HasConfig {
		t.Fatalf("state after create: %+v", created.State)
	}

	// Re-create is idempotent and must NOT return the backup again.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/create", nil))
	if rec.Code != 200 {
		t.Fatalf("re-create status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "owner_mnemonic") {
		t.Fatal("re-create leaked the backup")
	}

	// Backup endpoint re-reads recovery data.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/backup", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "owner_mnemonic") {
		t.Fatalf("backup status %d: %s", rec.Code, rec.Body)
	}

	// QR endpoint renders a PNG.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/wallet/qr.png", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("qr status %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestAppWalletImportConflictsWithExisting(t *testing.T) {
	a := newTestAppAPI(t)
	mux := serveAppAPI(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/create", nil))
	if rec.Code != 200 {
		t.Fatalf("create status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/import",
		strings.NewReader(`{"mnemonic":"a b c"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("import over existing wallet: status %d, want 409", rec.Code)
	}
}

func TestAppWalletImportBackupJSON(t *testing.T) {
	// Create a wallet in one dir, import its backup into another.
	source := newTestAppAPI(t)
	srcMux := serveAppAPI(source)
	rec := httptest.NewRecorder()
	srcMux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/create", nil))
	var created struct {
		Backup struct {
			FundAddress string `json:"fund_address"`
			BackupJSON  string `json:"backup_json"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	dest := newTestAppAPI(t)
	destMux := serveAppAPI(dest)
	payload, _ := json.Marshal(map[string]string{"backup_json": created.Backup.BackupJSON})
	rec = httptest.NewRecorder()
	destMux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wallet/import", strings.NewReader(string(payload))))
	if rec.Code != 200 {
		t.Fatalf("import status %d: %s", rec.Code, rec.Body)
	}
	var imported struct {
		Backup struct {
			FundAddress string `json:"fund_address"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Backup.FundAddress != created.Backup.FundAddress {
		t.Fatalf("fund address changed on restore: %q vs %q", imported.Backup.FundAddress, created.Backup.FundAddress)
	}
}

func TestAppEngineStartWithoutConfig(t *testing.T) {
	mux := serveAppAPI(newTestAppAPI(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/engine/start", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestLogRing(t *testing.T) {
	r := newLogRing(3)
	_, _ = r.Write([]byte("one\ntwo\n"))
	_, _ = r.Write([]byte("thr"))
	_, _ = r.Write([]byte("ee\nfour\n"))
	got := r.Snapshot()
	want := []string{"two", "three", "four"}
	if len(got) != len(want) {
		t.Fatalf("lines = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lines = %#v, want %#v", got, want)
		}
	}
}
