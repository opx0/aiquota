package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"aiquota/internal/model"
)

func toggleTestApp(t *testing.T, extra map[string]any) *app {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aiquota")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func toggleTestScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "collector")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitToggleCondition(t *testing.T, desc string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestToggleDisabledProvidersSkipCollectorsAndRetainCache(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	script := toggleTestScript(t, fmt.Sprintf("echo called >> %q\nexit 9", log))
	a := toggleTestApp(t, map[string]any{"codex_path": script, "agy_path": script})
	for _, id := range []string{"claude", "codex", "antigravity"} {
		p := base(id)
		p.Status = "healthy"
		p.Plan = "retained-secret"
		p.Windows = []model.Window{{ID: "cached"}}
		if err := a.store.Update(id, func(*model.Provider) (model.Provider, error) { return p, nil }); err != nil {
			t.Fatal(err)
		}
		if err := a.setProvider(id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.refresh(context.Background(), ""); err != nil {
		t.Fatalf("refresh all disabled: %v", err)
	}
	for _, id := range []string{"codex", "antigravity", "claude"} {
		if err := a.refresh(context.Background(), id); !errors.Is(err, errProviderDisabled) {
			t.Errorf("refresh %s: %v", id, err)
		}
	}
	if err := a.ingestClaude(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":90}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled collector ran: %v", err)
	}
	for _, p := range a.snapshot().Providers {
		if p.Enabled || p.Status != "disabled" || p.Plan != "" || len(p.Windows) != 0 {
			t.Errorf("disabled snapshot exposed cache: %+v", p)
		}
		cached, err := a.store.Read(p.ID)
		if err != nil || cached.Plan != "retained-secret" || len(cached.Windows) != 1 {
			t.Errorf("cache changed for %s: %+v, %v", p.ID, cached, err)
		}
		if err := a.setProvider(p.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range a.snapshot().Providers {
		if !p.Enabled || p.Plan != "retained-secret" || len(p.Windows) != 1 {
			t.Errorf("cache not restored: %+v", p)
		}
	}
}

func TestSnapshotEnabledFollowsConfigNotCachedRecord(t *testing.T) {
	a := toggleTestApp(t, map[string]any{})
	for _, id := range []string{"claude", "codex", "antigravity"} {
		p := base(id)
		p.Enabled = false // Older or failed records can carry a false enablement value.
		p.Status = "healthy"
		p.Plan = "retained"
		if err := a.store.Update(id, func(*model.Provider) (model.Provider, error) { return p, nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range a.snapshot().Providers {
		if !p.Enabled || p.Plan != "retained" {
			t.Errorf("enabled configuration did not override cached enabled=false: %+v", p)
		}
		cached, err := a.store.Read(p.ID)
		if err != nil || cached.Enabled {
			t.Errorf("snapshot modified cached record for %s: %+v, %v", p.ID, cached, err)
		}
	}
	if err := a.setProvider("codex", false); err != nil {
		t.Fatal(err)
	}
	for _, p := range a.snapshot().Providers {
		if p.ID == "codex" && (p.Enabled || p.Status != "disabled" || p.Plan != "") {
			t.Errorf("disabled configuration not authoritative: %+v", p)
		}
	}
	if err := a.setProvider("codex", true); err != nil {
		t.Fatal(err)
	}
	for _, p := range a.snapshot().Providers {
		if p.ID == "codex" && (!p.Enabled || p.Plan != "retained") {
			t.Errorf("re-enabled provider did not restore authoritative state: %+v", p)
		}
	}
}

func TestDaemonProviderReenableResetsBackoffAndRespectsInterval(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	script := toggleTestScript(t, fmt.Sprintf("echo call >> %q\nif [ -e %q ]; then exit 1; fi\nprintf '%%s' '{\"status\":\"SUCCESS\",\"num_turns\":0,\"command\":{\"name\":\"usage\",\"data\":{\"groups\":[{\"buckets\":[{\"id\":\"x\",\"remaining_fraction\":0.5}]}]}}}'", calls, fail))
	a := toggleTestApp(t, map[string]any{"agy_path": script})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.daemonProvider(ctx, "antigravity", time.Second) }()
	defer func() { cancel(); <-done }()
	callCount := func() int {
		b, err := os.ReadFile(calls)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "call\n")
	}
	waitToggleCondition(t, "initial failed daemon collection", 3*time.Second, func() bool { return callCount() >= 1 })
	if err := a.setProvider("antigravity", false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	// The worker reaches its first scheduled retry after two seconds; keep the
	// provider disabled past that point so the disabled state is observed.
	time.Sleep(2300 * time.Millisecond)
	if got := callCount(); got != 1 {
		t.Fatalf("disabled worker collected %d times", got)
	}
	if err := a.setProvider("antigravity", true); err != nil {
		t.Fatal(err)
	}
	waitToggleCondition(t, "prompt collection after re-enable", 6*time.Second, func() bool { return callCount() >= 2 })
	waitToggleCondition(t, "interval collection after backoff reset", 2500*time.Millisecond, func() bool { return callCount() >= 3 })
	if err := a.setProvider("antigravity", false); err != nil {
		t.Fatal(err)
	}
	n := callCount()
	time.Sleep(1200 * time.Millisecond)
	if got := callCount(); got != n {
		t.Fatalf("worker collected after disable: %d -> %d", n, got)
	}
}

func TestToggleConcurrentProvidersPreserveUpdatesAndAtomicJSON(t *testing.T) {
	a := toggleTestApp(t, map[string]any{"color": "never", "warning_threshold": 17, "timeout": "9s"})
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 4)
	for _, id := range []string{"claude", "codex", "antigravity"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			for i := 0; i < 20; i++ {
				if err := a.setProvider(id, false); err != nil {
					errs <- err
					return
				}
			}
		}(id)
	}
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		<-start
		for {
			select {
			case <-stop:
				return
			default:
				b, err := os.ReadFile(a.configPath)
				if err != nil {
					errs <- err
					return
				}
				var v map[string]any
				if err := json.Unmarshal(b, &v); err != nil {
					errs <- err
					return
				}
			}
		}
	}()
	close(start)
	wg.Wait()
	close(stop)
	<-readerDone
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	current, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	if current.cfg.Color != "never" || current.cfg.Timeout != "9s" || current.cfg.WarningThreshold == nil || *current.cfg.WarningThreshold != 17 {
		t.Fatalf("other settings lost: %+v", current.cfg)
	}
	if len(current.cfg.Disabled) != 3 {
		t.Fatalf("lost or duplicated updates: %v", current.cfg.Disabled)
	}
	for _, id := range []string{"claude", "codex", "antigravity"} {
		if current.enabled(id) {
			t.Errorf("%s unexpectedly enabled", id)
		}
	}
	info, err := os.Stat(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("config permissions %o", info.Mode().Perm())
	}
}

func TestToggleDisableWaitsForRealCollectorAndRejectsQueuedRefresh(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	calls := filepath.Join(dir, "calls")
	script := toggleTestScript(t, fmt.Sprintf("echo call >> %q\ntouch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf '%%s' '{\"status\":\"SUCCESS\",\"num_turns\":0,\"command\":{\"name\":\"usage\",\"data\":{\"groups\":[{\"name\":\"Gemini\",\"buckets\":[{\"id\":\"x\",\"remaining_fraction\":0.5}]}]}}}'", calls, started, release))
	a := toggleTestApp(t, map[string]any{"agy_path": script, "timeout": "5s"})
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- a.refresh(context.Background(), "antigravity") }()
	waitToggleCondition(t, "collector startup", 3*time.Second, func() bool { _, err := os.Stat(started); return err == nil })
	disabled := make(chan error, 1)
	go func() { disabled <- a.setProvider("antigravity", false) }()
	select {
	case err := <-disabled:
		t.Fatalf("disable returned during collection: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if err := a.refresh(context.Background(), "antigravity"); !errors.Is(err, errProviderDisabled) {
		t.Fatalf("post-disable refresh: %v", err)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "call") != 1 {
		t.Fatalf("calls: %q", b)
	}
	p, err := a.store.Read("antigravity")
	if err != nil || p.Status != "healthy" {
		t.Fatalf("in-flight cache not retained: %+v %v", p, err)
	}
}

func TestToggleInvalidConfigFailsClosedForExistingApp(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	script := toggleTestScript(t, fmt.Sprintf("echo called >> %q", log))
	a := toggleTestApp(t, map[string]any{"codex_path": script, "agy_path": script})
	if err := os.WriteFile(a.configPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.refresh(context.Background(), ""); err == nil {
		t.Error("refresh accepted invalid config")
	}
	if err := a.ingestClaude(strings.NewReader(`{}`)); err == nil {
		t.Error("ingest accepted invalid config")
	}
	if err := a.setProvider("codex", false); err == nil {
		t.Error("toggle accepted invalid config")
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("collector ran: %v", err)
	}
	for _, p := range a.snapshot().Providers {
		if p.Enabled || p.Status != "failed" {
			t.Errorf("snapshot did not fail closed: %+v", p)
		}
	}
}

func TestToggleDoctorSkipsDisabledCodexAuthentication(t *testing.T) {
	if os.Getenv("AIQUOTA_TOGGLE_DOCTOR_HELPER") == "1" {
		os.Args = []string{"aiquota", "doctor"}
		main()
		return
	}
	log := filepath.Join(t.TempDir(), "calls")
	script := toggleTestScript(t, fmt.Sprintf("echo called >> %q", log))
	toggleTestApp(t, map[string]any{"codex_path": script, "disabled": []string{"codex"}})
	cmd := exec.Command(os.Args[0], "-test.run=^TestToggleDoctorSkipsDisabledCodexAuthentication$")
	cmd.Env = append(os.Environ(), "AIQUOTA_TOGGLE_DOCTOR_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Codex authentication: skipped (provider disabled)") {
		t.Fatalf("doctor: %v, %s", err, out)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled Codex diagnostic ran: %v", err)
	}
}

func TestToggleBlockedClaudeIngestCompletesBeforeDisable(t *testing.T) {
	a := toggleTestApp(t, map[string]any{})
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ingested := make(chan error, 1)
	go func() { ingested <- a.ingestClaude(r) }()
	// A write succeeds only when ingestion has acquired the gate and started reading.
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, `{"rate_limits":{"five_hour":{"used_percentage":25}}}`)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Claude ingestion did not begin reading")
	}
	disabled := make(chan error, 1)
	go func() { disabled <- a.setProvider("claude", false) }()
	select {
	case err := <-disabled:
		t.Fatalf("disable returned while ingestion held gate: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-ingested; err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(a.store.Dir, "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ingestClaude(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":90}}}`)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(a.store.Dir, "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("post-disable ingestion modified cache")
	}
}

func TestToggleDisabledClaudeWrapPreservesOriginalAndCache(t *testing.T) {
	if os.Getenv("AIQUOTA_TOGGLE_WRAP_HELPER") == "1" {
		os.Args = []string{"aiquota", "claude-wrap", `cat; printf original-error >&2; exit 7`}
		main()
		return
	}
	a := toggleTestApp(t, map[string]any{"disabled": []string{"claude"}})
	p := base("claude")
	p.Plan = "cached"
	if err := a.store.Update("claude", func(*model.Provider) (model.Provider, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(a.store.Dir, "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	input := `{"rate_limits":{"five_hour":{"used_percentage":90}}}`
	cmd := exec.Command(os.Args[0], "-test.run=^TestToggleDisabledClaudeWrapPreservesOriginalAndCache$")
	cmd.Env = append(os.Environ(), "AIQUOTA_TOGGLE_WRAP_HELPER=1")
	cmd.Stdin = strings.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || out.String() != input || stderr.String() != "original-error" {
		t.Fatalf("wrapper exit %v stdout %q stderr %q", err, out.String(), stderr.String())
	}
	after, err := os.ReadFile(filepath.Join(a.store.Dir, "claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("disabled wrapper modified Claude cache")
	}
}

func TestToggleDaemonSeesEnableAndDisableWithoutRestart(t *testing.T) {
	if os.Getenv("AIQUOTA_TOGGLE_DAEMON_HELPER") == "1" {
		os.Args = []string{"aiquota", "daemon"}
		main()
		return
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := toggleTestScript(t, fmt.Sprintf("echo called >> %q\nprintf '%%s' '{\"status\":\"SUCCESS\",\"num_turns\":0,\"command\":{\"name\":\"usage\",\"data\":{\"groups\":[{\"buckets\":[{\"id\":\"x\",\"remaining_fraction\":0.5}]}]}}}'", calls))
	a := toggleTestApp(t, map[string]any{"disabled": []string{"codex", "antigravity"}, "agy_path": script})
	cmd := exec.Command(os.Args[0], "-test.run=^TestToggleDaemonSeesEnableAndDisableWithoutRestart$")
	cmd.Env = append(os.Environ(), "AIQUOTA_TOGGLE_DAEMON_HELPER=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	waitToggleCondition(t, "daemon lock", 3*time.Second, a.daemonRunning)
	// Let the disabled worker reach its five-second retry, then enable it.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled daemon invoked collector: %v", err)
	}
	if err := a.setProvider("antigravity", true); err != nil {
		t.Fatal(err)
	}
	waitToggleCondition(t, "enabled daemon collection", 7*time.Second, func() bool { _, err := os.Stat(calls); return err == nil })
	if err := a.setProvider("antigravity", false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.refresh(context.Background(), "antigravity"); !errors.Is(err, errProviderDisabled) {
		t.Fatalf("disabled refresh: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	after, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("collection after disable: %q -> %q", before, after)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("daemon exit %v: %s", err, stderr.String())
	}
}
