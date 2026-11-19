package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"aiquota/internal/model"
	"aiquota/internal/state"
)

func TestCachedStatusAndRendering(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a := app{store: state.Store{Dir: dir}, stale: time.Hour}
	got := a.snapshot()
	if len(got.Providers) != 3 || (got.Providers[2].Status != "unavailable") {
		t.Fatalf("unexpected status: %+v", got)
	}
	b, err := json.Marshal(got)
	if err != nil || !json.Valid(b) {
		t.Fatalf("invalid JSON: %v", err)
	}
	var out bytes.Buffer
	render(&out, got)
	if !strings.Contains(out.String(), "Claude Code") || !strings.Contains(out.String(), "Antigravity") {
		t.Fatal(out.String())
	}
}
func TestOldObservationIsStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	a := app{store: state.Store{Dir: dir}, stale: time.Minute}
	then := time.Now().Add(-time.Hour)
	err := a.store.Update("claude", func(*model.Provider) (model.Provider, error) {
		p := base("claude")
		p.Status = "healthy"
		p.ObservedAt = &then
		return p, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.snapshot().Providers[0].Status; got != "stale" {
		t.Fatalf("got %q", got)
	}
}

func TestConfigRejectsTrailingJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aiquota")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{} {"timeout":"1s"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newApp(); err == nil {
		t.Fatal("accepted multiple JSON documents")
	}
}

func TestProviderTogglePreservesConfigAndCachedData(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aiquota")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"color":"never","disabled":["codex"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	p := base("claude")
	p.Status = "healthy"
	p.Plan = "cached"
	if err := a.store.Update("claude", func(*model.Provider) (model.Provider, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.setProvider("claude", false); err != nil {
		t.Fatal(err)
	}
	got := a.snapshot().Providers[0]
	if got.Enabled || got.Status != "disabled" || got.Plan != "" || len(got.Windows) != 0 {
		t.Fatalf("disabled snapshot exposed data: %+v", got)
	}
	if err := a.ingestClaude(strings.NewReader("invalid")); err != nil {
		t.Fatalf("disabled ingestion: %v", err)
	}
	if err := a.setProvider("claude", true); err != nil {
		t.Fatal(err)
	}
	current, err := newApp()
	if err != nil || current.cfg.Color != "never" || current.enabled("codex") || !current.enabled("claude") {
		t.Fatalf("config not preserved: %+v, %v", current, err)
	}
	if got := a.snapshot().Providers[0]; got.Plan != "cached" || !got.Enabled {
		t.Fatalf("cached data not retained: %+v", got)
	}
}

func TestSnapshotEnabledComesFromConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	p := base("claude")
	p.Enabled = false
	if err := a.store.Update("claude", func(*model.Provider) (model.Provider, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	if got := a.snapshot().Providers[0]; !got.Enabled {
		t.Fatalf("enabled config not reflected in snapshot: %+v", got)
	}
}

func TestProviderDisableWaitsForGate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	gate, _, err := a.providerGate("codex")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.setProvider("codex", false) }()
	select {
	case err := <-done:
		gate.Close()
		t.Fatalf("disable returned while collection held gate: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	gate.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.providerGate("codex"); !errors.Is(err, errProviderDisabled) {
		t.Fatalf("collection gate after disable: %v", err)
	}
}

func TestProviderSpecificRefreshExplainsUnavailableCollection(t *testing.T) {
	a := &app{store: state.Store{Dir: t.TempDir()}, timeout: time.Second}
	for _, id := range []string{"claude"} {
		if err := a.refresh(context.Background(), id); err == nil {
			t.Errorf("refresh %s succeeded without collecting data", id)
		}
	}
	a.cfg.Disabled = []string{"codex"}
	if err := a.refresh(context.Background(), "codex"); err == nil {
		t.Fatal("refresh of disabled provider succeeded")
	}
}

func TestRefreshFailurePreservesLastSuccess(t *testing.T) {
	a := &app{store: state.Store{Dir: filepath.Join(t.TempDir(), "state")}, timeout: time.Second, cfg: config{CodexPath: filepath.Join(t.TempDir(), "missing")}}
	observed := time.Now().Add(-time.Minute).UTC()
	used := 25.0
	previous := base("codex")
	previous.Status = "healthy"
	previous.ObservedAt = &observed
	previous.LastSuccess = &observed
	previous.Windows = []model.Window{{ID: "bucket", UsedPercent: &used}}
	if err := a.store.Update("codex", func(*model.Provider) (model.Provider, error) { return previous, nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.refresh(context.Background(), "codex"); err == nil {
		t.Fatal("expected refresh failure")
	}
	got, err := a.store.Read("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || len(got.Windows) != 1 || got.Windows[0].ID != "bucket" || got.LastSuccess == nil || !got.LastSuccess.Equal(observed) || got.LastAttempt == nil {
		t.Fatalf("last successful observation lost: %+v", got)
	}
}

func TestClaudeStatusDiagnosisDoesNotExposeCommand(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ content, want string }{
		{`{}`, "not configured"},
		{`{"statusLine":{"type":"command","command":"aiquota ingest-claude"}}`, "reference aiquota"},
		{`{"statusLine":{"type":"command","command":"private-user-script secret"}}`, "custom status line configured"},
		{`not json`, "cannot read"},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		got := claudeStatus(dir)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "secret") {
			t.Errorf("%q produced %q", tc.content, got)
		}
	}
}

func TestDoctorChecksStateWithoutLeavingFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if got := stateStatus(dir); !strings.Contains(got, "writable") {
		t.Fatal(got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("doctor left files: %v", entries)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if got := stateStatus(link); !strings.Contains(got, "not a private directory") {
		t.Fatal(got)
	}
}

func TestRenderIncludesCreditsAndSpendAmounts(t *testing.T) {
	balance := "7.5"
	used := 2.0
	limit := 10.0
	p := base("claude")
	p.Credits = &model.Credits{Balance: &balance}
	p.Windows = []model.Window{{Label: "Spend", UsedAmount: &used, AllocationLimit: &limit, Unit: "USD (estimated)"}}
	var out bytes.Buffer
	render(&out, output{Providers: []model.Provider{p}})
	for _, want := range []string{"7.5", "2", "10", "USD (estimated)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %q", want, out.String())
		}
	}
}

func TestLockRejectsUnsafePaths(t *testing.T) {
	baseDir := t.TempDir()
	dir := filepath.Join(baseDir, "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(baseDir, "link")
	if err := os.Symlink(dir, symlink); err != nil {
		t.Fatal(err)
	}
	if f, err := lock(symlink, "daemon.lock", true); err == nil {
		f.Close()
		t.Error("accepted symlink state directory")
	}
	if err := os.Mkdir(filepath.Join(dir, "daemon.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if f, err := lock(dir, "daemon.lock", true); err == nil {
		f.Close()
		t.Error("accepted directory as lock")
	}
	unsafe := filepath.Join(baseDir, "public")
	if err := os.Mkdir(unsafe, 0755); err != nil {
		t.Fatal(err)
	}
	if f, err := lock(unsafe, "daemon.lock", true); err == nil {
		f.Close()
		t.Error("accepted public state directory")
	}
}

func TestRenderShowsModelCountdownAndWarnings(t *testing.T) {
	remaining := 12.0
	reset := time.Date(2030, 1, 2, 15, 4, 0, 0, time.UTC)
	now := reset.Add(-2*time.Hour - 5*time.Minute)
	o := output{Providers: []model.Provider{{Name: "Codex", Status: "healthy", Windows: []model.Window{{Label: "short", Model: "example-model", RemainingPercent: &remaining, ResetsAt: &reset}}}}}
	var out bytes.Buffer
	renderAt(&out, o, renderOptions{now: now, color: true, warningThreshold: 20, width: 60})
	s := out.String()
	for _, want := range []string{"example-model", "in 2h 5m", "\x1b[33m", "12% remaining"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q: %s", want, s)
		}
	}
}

func TestColorConfigurationAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if useColor("always", true) {
		t.Fatal("NO_COLOR must disable color")
	}
	t.Setenv("NO_COLOR", "")
	if useColor("never", true) {
		t.Fatal("never enabled color")
	}
	if !useColor("always", false) {
		t.Fatal("always did not enable color")
	}
}

func TestConfigValidatesPresentation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aiquota")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"color":"auto","warning_threshold":0}`, true},
		{`{"color":"sometimes"}`, false},
		{`{"warning_threshold":-1}`, false},
		{`{"warning_threshold":101}`, false},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := newApp()
		if (err == nil) != tc.valid {
			t.Errorf("config %s: error %v", tc.body, err)
		}
	}
}

func TestClaudeWrapProcess(t *testing.T) {
	if os.Getenv("AIQUOTA_TEST_WRAP_HELPER") == "1" {
		os.Args = []string{"aiquota", "claude-wrap", `printf original-error >&2; exit 7`}
		main()
		return
	}
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "aiquota")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("bad config"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeWrapProcess$")
	cmd.Env = append(os.Environ(), "AIQUOTA_TEST_WRAP_HELPER=1", "XDG_STATE_HOME="+t.TempDir(), "XDG_CONFIG_HOME="+configRoot)
	cmd.Stdin = strings.NewReader(`{}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit: %v", err)
	}
	if stdout.String() != "" || stderr.String() != "original-error" {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestFutureObservationIsStale(t *testing.T) {
	a := app{store: state.Store{Dir: filepath.Join(t.TempDir(), "state")}, stale: time.Hour}
	then := time.Now().Add(time.Hour)
	if err := a.store.Update("claude", func(*model.Provider) (model.Provider, error) {
		p := base("claude")
		p.Status = "healthy"
		p.ObservedAt = &then
		return p, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.snapshot().Providers[0].Status; got != "stale" {
		t.Fatalf("clock moved backwards: status = %q", got)
	}
}

func TestDaemonOwnershipAndCancellation(t *testing.T) {
	if os.Getenv("AIQUOTA_TEST_DAEMON_HELPER") == "1" {
		os.Args = []string{"aiquota", "daemon"}
		main()
		return
	}
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "aiquota")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"disabled":["codex","antigravity"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	env := append(os.Environ(), "AIQUOTA_TEST_DAEMON_HELPER=1", "XDG_CONFIG_HOME="+configRoot, "XDG_STATE_HOME="+stateRoot)
	start := func() *exec.Cmd {
		c := exec.Command(os.Args[0], "-test.run=^TestDaemonOwnershipAndCancellation$")
		c.Env = env
		return c
	}
	first := start()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if first.ProcessState == nil {
			first.Process.Kill()
			first.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := lock(filepath.Join(stateRoot, "aiquota"), "daemon.lock", true)
		if err != nil && (errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)) {
			break
		}
		if err == nil {
			f.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not acquire lock: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := start()
	if err := second.Run(); err == nil {
		t.Fatal("second daemon acquired the same lock")
	}
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("daemon did not shut down cleanly: %v", err)
	}
}

func TestRetryDelay(t *testing.T) {
	for _, tc := range []struct{ current, interval, want time.Duration }{
		{2 * time.Minute, 2 * time.Minute, 4 * time.Minute},
		{20 * time.Minute, 2 * time.Minute, 30 * time.Minute},
		{45 * time.Minute, 45 * time.Minute, 45 * time.Minute},
	} {
		if got := retryDelay(tc.current, tc.interval); got != tc.want {
			t.Errorf("retryDelay(%s,%s)=%s, want %s", tc.current, tc.interval, got, tc.want)
		}
	}
}

func TestAntigravityRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agy")
	script := `#!/bin/sh
printf '%s' '{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"name":"Gemini","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.5}]}]}}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := &app{store: state.Store{Dir: filepath.Join(dir, "state")}, timeout: time.Second, cfg: config{AgyPath: path}}
	if err := a.refresh(context.Background(), "antigravity"); err != nil {
		t.Fatal(err)
	}
	p, err := a.store.Read("antigravity")
	if err != nil || len(p.Windows) != 1 || *p.Windows[0].RemainingPercent != 50 {
		t.Fatalf("got %+v: %v", p, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := a.refresh(context.Background(), "antigravity"); err == nil {
		t.Fatal("expected failure")
	}
	p, err = a.store.Read("antigravity")
	if err != nil || p.Status != "failed" || len(p.Windows) != 1 {
		t.Fatalf("previous observation lost: %+v %v", p, err)
	}
}

func TestRefreshAllIsolatesProviders(t *testing.T) {
	dir := t.TempDir()
	agy := filepath.Join(dir, "agy")
	script := `#!/bin/sh
printf '%s' '{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"name":"Gemini","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.5}]}]}}}'
`
	if err := os.WriteFile(agy, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := &app{store: state.Store{Dir: filepath.Join(dir, "state")}, timeout: time.Second, cfg: config{CodexPath: filepath.Join(dir, "missing"), AgyPath: agy}}
	if err := a.refresh(context.Background(), ""); err == nil {
		t.Fatal("expected Codex failure")
	}
	p, err := a.store.Read("antigravity")
	if err != nil || p.Status != "healthy" || len(p.Windows) != 1 {
		t.Fatalf("Antigravity was not refreshed: %+v %v", p, err)
	}
}

func TestTerminalDetectionRejectsFilesAndPipes(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "terminal-check")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("regular file reported as a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) {
		t.Fatal("pipe reported as a terminal")
	}
}

func TestDefaultCommandFallsBackToTextWithoutTerminal(t *testing.T) {
	if os.Getenv("AIQUOTA_TEST_DEFAULT_HELPER") == "1" {
		os.Args = []string{"aiquota"}
		main()
		return
	}
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "aiquota")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"disabled":["codex","antigravity"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDefaultCommandFallsBackToTextWithoutTerminal$")
	cmd.Env = append(os.Environ(), "AIQUOTA_TEST_DEFAULT_HELPER=1", "XDG_CONFIG_HOME="+configRoot, "XDG_STATE_HOME="+t.TempDir(), "NO_COLOR=1")
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("default command failed: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Claude Code") || !strings.Contains(out, "status-line") {
		t.Fatalf("expected text status, got:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("text fallback emitted ANSI escapes: %q", out)
	}
}
