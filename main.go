package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/mattn/go-isatty"

	"aiquota/internal/antigravity"
	"aiquota/internal/claude"
	"aiquota/internal/codex"
	"aiquota/internal/model"
	"aiquota/internal/state"
	"aiquota/internal/tui"
)

var version = "dev"

type config struct {
	AgyPath             string   `json:"agy_path"`
	AntigravityInterval string   `json:"antigravity_interval"`
	CodexPath           string   `json:"codex_path"`
	CodexInterval       string   `json:"codex_interval"`
	Timeout             string   `json:"timeout"`
	StaleAfter          string   `json:"stale_after"`
	Disabled            []string `json:"disabled"`
	Color               string   `json:"color"`
	WarningThreshold    *float64 `json:"warning_threshold"`
}
type app struct {
	store                                 state.Store
	cfg                                   config
	configPath                            string
	interval, agyInterval, timeout, stale time.Duration
}
type output struct {
	SchemaVersion int              `json:"schema_version"`
	Providers     []model.Provider `json:"providers"`
}

func homeDir(env, fallback string) string {
	if p := os.Getenv(env); filepath.IsAbs(p) {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, fallback)
}
func newApp() (*app, error) {
	a := &app{store: state.Store{Dir: filepath.Join(homeDir("XDG_STATE_HOME", ".local/state"), "aiquota")}, interval: 2 * time.Minute, agyInterval: 5 * time.Minute, timeout: 15 * time.Second, stale: 15 * time.Minute}
	p := filepath.Join(homeDir("XDG_CONFIG_HOME", ".config"), "aiquota", "config.json")
	a.configPath = p
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		trimmed := strings.TrimSpace(string(b))
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, errors.New("config: expected JSON object")
		}
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.DisallowUnknownFields()
		if err = d.Decode(&a.cfg); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		if err = d.Decode(&struct{}{}); err != io.EOF {
			return nil, errors.New("config: expected one JSON object")
		}
	}
	for _, v := range []struct {
		s   string
		dst *time.Duration
	}{{a.cfg.CodexInterval, &a.interval}, {a.cfg.AntigravityInterval, &a.agyInterval}, {a.cfg.Timeout, &a.timeout}, {a.cfg.StaleAfter, &a.stale}} {
		if v.s != "" {
			n, e := time.ParseDuration(v.s)
			if e != nil || n < time.Second {
				return nil, fmt.Errorf("invalid duration %q (minimum 1s)", v.s)
			}
			*v.dst = n
		}
	}
	if a.interval < time.Minute {
		return nil, errors.New("codex_interval must be at least 1m")
	}
	if a.agyInterval < 2*time.Minute {
		return nil, errors.New("antigravity_interval must be at least 2m")
	}
	if a.cfg.Color != "" && a.cfg.Color != "auto" && a.cfg.Color != "always" && a.cfg.Color != "never" {
		return nil, errors.New("color must be auto, always, or never")
	}
	if a.cfg.WarningThreshold != nil && (*a.cfg.WarningThreshold < 0 || *a.cfg.WarningThreshold > 100) {
		return nil, errors.New("warning_threshold must be between 0 and 100")
	}
	for _, id := range a.cfg.Disabled {
		if id != "claude" && id != "codex" && id != "antigravity" {
			return nil, fmt.Errorf("unknown disabled provider %q", id)
		}
	}
	return a, nil
}
func (a *app) enabled(id string) bool {
	for _, v := range a.cfg.Disabled {
		if v == id {
			return false
		}
	}
	return true
}
func base(id string) model.Provider {
	p := model.Provider{ID: id, Enabled: true}
	switch id {
	case "claude":
		p.Name = "Claude Code"
		p.Collection = "status-line"
		p.Status = "unavailable"
		p.Message = "Waiting for Claude Code status-line data"
	case "codex":
		p.Name = "Codex"
		p.Collection = "app-server"
		p.Status = "unavailable"
		p.Message = "Run aiquota refresh to collect quota"
	case "antigravity":
		p.Name = "Antigravity"
		p.Collection = "agy usage"
		p.Status = "unavailable"
		p.Message = "Run aiquota refresh antigravity to collect quota"
	}
	return p
}
func (a *app) snapshot() output {
	out := output{SchemaVersion: 1, Providers: []model.Provider{}}
	if a.configPath != "" {
		current, err := newApp()
		if err != nil {
			// Keep controls available, but do not expose cached values or collect.
			for _, id := range []string{"claude", "codex", "antigravity"} {
				p := base(id)
				p.Enabled, p.Status, p.Message = false, "failed", "Cannot read configuration: "+err.Error()
				out.Providers = append(out.Providers, p)
			}
			return out
		}
		a = current
	}
	for _, id := range []string{"claude", "codex", "antigravity"} {
		if !a.enabled(id) {
			p := base(id)
			p.Enabled, p.Status, p.Message = false, "disabled", "Collection disabled"
			out.Providers = append(out.Providers, p)
			continue
		}
		p, err := a.store.Read(id)
		if err != nil {
			p = base(id)
			if !errors.Is(err, os.ErrNotExist) {
				p.Status = "failed"
				p.Message = "Cannot read cached state: " + err.Error()
			}
		}
		p.Enabled = true
		if p.Status == "healthy" {
			now := time.Now()
			stale := p.ObservedAt != nil && (now.Sub(*p.ObservedAt) > a.stale || p.ObservedAt.After(now.Add(time.Minute)))
			// Claude only reports when its status-line command runs. Its data remains
			// useful between interactions while the reported quota windows are active.
			if p.ID == "claude" && hasActiveQuotaWindow(p.Windows, now) {
				stale = false
			}
			for _, w := range p.Windows {
				if p.ID != "claude" && w.ResetsAt != nil && !now.Before(*w.ResetsAt) {
					stale = true
				}
			}
			if stale {
				p.Status = "stale"
			}
		}
		out.Providers = append(out.Providers, p)
	}
	return out
}

func hasActiveQuotaWindow(windows []model.Window, now time.Time) bool {
	for _, w := range windows {
		if w.ResetsAt != nil && w.ResetsAt.After(now) {
			return true
		}
	}
	return false
}
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("not a private directory; use a directory owned by your user with mode 0700")
	}
	return nil
}

func lock(dir, name string, nonblock bool) (*os.File, error) {
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Geteuid() {
		f.Close()
		return nil, errors.New("lock is not a private regular file")
	}
	how := syscall.LOCK_EX
	if nonblock {
		how |= syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

var errProviderDisabled = errors.New("provider disabled in configuration")

// The provider gate serializes collection with enablement changes across processes.
func (a *app) providerGate(id string) (*os.File, *app, error) {
	l, err := lock(a.store.Dir, id+"-gate.lock", false)
	if err != nil {
		return nil, nil, err
	}
	current := a
	if a.configPath != "" {
		current, err = newApp()
		if err != nil {
			l.Close()
			return nil, nil, err
		}
	}
	if !current.enabled(id) {
		l.Close()
		return nil, nil, errProviderDisabled
	}
	return l, current, nil
}

func (a *app) setProvider(id string, enabled bool) error {
	if id != "claude" && id != "codex" && id != "antigravity" {
		return fmt.Errorf("unknown provider %q", id)
	}
	gate, err := lock(a.store.Dir, id+"-gate.lock", false)
	if err != nil {
		return err
	}
	defer gate.Close()
	configLock, err := lock(a.store.Dir, "config.lock", false)
	if err != nil {
		return err
	}
	defer configLock.Close()
	current, err := newApp()
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	b, err := os.ReadFile(current.configPath)
	if errors.Is(err, os.ErrNotExist) {
		raw = make(map[string]json.RawMessage)
	} else if err != nil {
		return err
	} else if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
		return errors.New("config: expected JSON object")
	}
	disabled := make([]string, 0, len(current.cfg.Disabled)+1)
	for _, v := range current.cfg.Disabled {
		if v != id {
			disabled = append(disabled, v)
		}
	}
	if !enabled {
		disabled = append(disabled, id)
	}
	raw["disabled"], err = json.Marshal(disabled)
	if err != nil {
		return err
	}
	b, err = json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(current.configPath)
	if err := privateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), current.configPath)
}

func (a *app) ingestClaude(r io.Reader) error {
	l, current, err := a.providerGate("claude")
	if errors.Is(err, errProviderDisabled) {
		return nil
	}
	if err != nil {
		return err
	}
	defer l.Close()
	p, err := claude.Ingest(r, time.Now().UTC())
	if err != nil {
		return err
	}
	return claude.Save(current.store, p)
}

func (a *app) refresh(ctx context.Context, id string) error {
	if id != "" && id != "codex" && id != "claude" && id != "antigravity" {
		return fmt.Errorf("unknown provider %q", id)
	}
	if id != "" && a.configPath == "" && !a.enabled(id) {
		return fmt.Errorf("%s is disabled in configuration", id)
	}
	if id == "claude" {
		gate, _, err := a.providerGate(id)
		if err != nil {
			return err
		}
		gate.Close()
		return errors.New("Claude updates are event-driven; configure the Claude status line and wait for an observation")
	}
	var errs []error
	for _, provider := range []string{"codex", "antigravity"} {
		if id == "" || id == provider {
			if err := a.refreshProvider(ctx, provider); err != nil {
				if id == "" && errors.Is(err, errProviderDisabled) {
					continue
				}
				errs = append(errs, fmt.Errorf("%s: %w", provider, err))
			}
		}
	}
	return errors.Join(errs...)
}
func (a *app) refreshProvider(ctx context.Context, id string) error {
	gate, current, err := a.providerGate(id)
	if err != nil {
		return err
	}
	defer gate.Close()
	a = current
	l, err := lock(a.store.Dir, id+"-refresh.lock", true)
	if err != nil {
		return fmt.Errorf("refresh already running or lock unavailable: %w", err)
	}
	defer l.Close()
	now := time.Now().UTC()
	c, cancel := context.WithTimeout(ctx, a.timeout)
	var p model.Provider
	var readErr error
	if id == "codex" {
		p, readErr = codex.Read(c, a.cfg.CodexPath)
	} else {
		p, readErr = antigravity.Read(c, a.cfg.AgyPath)
	}
	cancel()
	saveErr := a.store.Update(id, func(old *model.Provider) (model.Provider, error) {
		if readErr == nil {
			return p, nil
		}
		q := base(id)
		if old != nil {
			q = *old
		}
		q.LastAttempt = &now
		q.Status = "failed"
		q.Message = "Refresh failed: " + readErr.Error()
		return q, nil
	})
	if saveErr != nil {
		return fmt.Errorf("save state: %w", saveErr)
	}
	return readErr
}
func (a *app) daemonRunning() bool {
	f, err := lock(a.store.Dir, "daemon.lock", true)
	if err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
	}
	f.Close()
	return false
}

func claudeStatus(dir string) string {
	f, err := os.Open(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "not configured in user settings"
	}
	if err != nil {
		return "cannot read user settings"
	}
	defer f.Close()
	var data struct {
		StatusLine *struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err := dec.Decode(&data); err != nil {
		return "cannot read user settings"
	}
	if data.StatusLine == nil {
		return "not configured in user settings"
	}
	if strings.Contains(data.StatusLine.Command, "aiquota") {
		return "user settings reference aiquota; verify the configured command actually invokes ingest-claude"
	}
	return "custom status line configured; add ingestion to its script if needed"
}

func stateStatus(dir string) string {
	if err := privateDir(dir); err != nil {
		return err.Error()
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return "not writable: " + err.Error()
	}
	name := f.Name()
	if err = f.Close(); err != nil {
		os.Remove(name)
		return "cannot close test file: " + err.Error()
	}
	if err = os.Remove(name); err != nil {
		return "cannot remove test file: " + err.Error()
	}
	return "writable and private"
}

func codexLoginStatus(ctx context.Context, executable string) string {
	if executable == "" {
		executable = "codex"
	}
	if _, err := exec.LookPath(executable); err != nil {
		return "not available"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "login", "status")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "check timed out"
		}
		return "not authenticated or login status could not be checked"
	}
	return "authenticated according to codex login status (account type not checked)"
}

type renderOptions struct {
	now              time.Time
	color            bool
	warningThreshold float64
	width            int
}

func useColor(mode string, terminal bool) bool {
	if os.Getenv("NO_COLOR") != "" || mode == "never" {
		return false
	}
	return mode == "always" || (terminal && mode != "never" && os.Getenv("TERM") != "dumb")
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func (a *app) runTUI(ctx context.Context) error {
	threshold := 20.0
	if a.cfg.WarningThreshold != nil {
		threshold = *a.cfg.WarningThreshold
	}
	daemonRunning := a.daemonRunning()
	return tui.Run(ctx, tui.Options{
		Load: func() ([]model.Provider, error) { return a.snapshot().Providers, nil },
		Refresh: func(ctx context.Context, id string) error {
			if a.daemonRunning() {
				return errors.New("the daemon is refreshing provider data; wait for the next cached update")
			}
			return a.refresh(ctx, id)
		},
		RefreshOnStart:   !daemonRunning,
		WarningThreshold: threshold,
		Color:            useColor(a.cfg.Color, true),
		Input:            os.Stdin,
		Output:           os.Stdout,
	})
}

func countdown(d time.Duration) string {
	return tui.Countdown(d)
}

func render(w io.Writer, o output) {
	renderAt(w, o, renderOptions{now: time.Now(), warningThreshold: 20})
}

func terminalWidth(w *os.File) int {
	size := struct{ rows, cols, x, y uint16 }{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, w.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	if errno == 0 && size.cols > 0 {
		return int(size.cols)
	}
	width, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	return width
}

func (a *app) render(w *os.File, o output) {
	terminal := false
	if info, err := w.Stat(); err == nil {
		terminal = info.Mode()&os.ModeCharDevice != 0
	}
	threshold := 20.0
	if a.cfg.WarningThreshold != nil {
		threshold = *a.cfg.WarningThreshold
	}
	renderAt(w, o, renderOptions{now: time.Now(), color: useColor(a.cfg.Color, terminal), warningThreshold: threshold, width: terminalWidth(w)})
}

func renderAt(w io.Writer, o output, opts renderOptions) {
	for _, p := range o.Providers {
		fmt.Fprintf(w, "%s  [%s]\n", p.Name, p.Status)
		if p.Plan != "" {
			fmt.Fprintf(w, "  Plan: %s\n", p.Plan)
		}
		if p.Credits != nil {
			if p.Credits.Unlimited != nil && *p.Credits.Unlimited {
				fmt.Fprintln(w, "  Credits: unlimited")
			} else if p.Credits.Balance != nil {
				fmt.Fprintf(w, "  Credits: %s\n", *p.Credits.Balance)
			} else if p.Credits.HasCredits != nil {
				fmt.Fprintf(w, "  Credits available: %t\n", *p.Credits.HasCredits)
			}
		}
		if p.Message != "" {
			fmt.Fprintf(w, "  %s\n", p.Message)
		}
		for _, q := range p.Windows {
			fmt.Fprintf(w, "  %s", q.Label)
			if q.Model != "" {
				fmt.Fprintf(w, " [%s]", q.Model)
			}
			if q.DurationMinutes != nil {
				fmt.Fprintf(w, " (%s)", (time.Duration(*q.DurationMinutes) * time.Minute).String())
			}
			if q.RemainingPercent != nil {
				n := int(*q.RemainingPercent / 10)
				if n < 0 {
					n = 0
				}
				if n > 10 {
					n = 10
				}
				if opts.color && *q.RemainingPercent <= opts.warningThreshold {
					fmt.Fprint(w, "\x1b[33m")
				}
				fmt.Fprintf(w, "  [%s%s] %.0f%% remaining", strings.Repeat("#", n), strings.Repeat("-", 10-n), *q.RemainingPercent)
				if opts.color && *q.RemainingPercent <= opts.warningThreshold {
					fmt.Fprint(w, "\x1b[0m")
				}
			} else {
				fmt.Fprint(w, "  unknown")
			}
			if q.UsedAmount != nil || q.AllocationLimit != nil {
				fmt.Fprint(w, "  ")
				if q.UsedAmount != nil {
					fmt.Fprintf(w, "%g used", *q.UsedAmount)
				}
				if q.AllocationLimit != nil {
					fmt.Fprintf(w, " / %g limit", *q.AllocationLimit)
				}
				if q.Unit != "" {
					fmt.Fprintf(w, " %s", q.Unit)
				}
			}
			if q.ResetsAt != nil {
				if opts.width > 0 && opts.width < 80 {
					fmt.Fprint(w, "\n   ")
				}
				fmt.Fprintf(w, "  resets %s", q.ResetsAt.Local().Format("02 Jan 15:04 MST"))
				if q.ResetsAt.After(opts.now) {
					fmt.Fprintf(w, " (in %s)", countdown(q.ResetsAt.Sub(opts.now)))
				} else {
					fmt.Fprint(w, " (passed; awaiting update)")
				}
			}
			fmt.Fprintln(w)
		}
		if p.ObservedAt != nil {
			fmt.Fprintf(w, "  Updated %s\n", p.ObservedAt.Local().Format("02 Jan 15:04 MST"))
		}
		fmt.Fprintln(w)
	}
}
func help() {
	fmt.Print(`AIQuota monitors coding subscription quotas.

Usage: aiquota [command]

Commands:
  (no command)               Open the interactive dashboard on a terminal.
  tui                        Open the interactive dashboard.
  status [--cached] [--json] Show providers; refresh active sources unless cached.
  refresh [codex|antigravity] Refresh supported providers.
  provider enable|disable ID Enable or disable collection for a provider.
  watch                      Print changed cached status until interrupted.
  doctor                     Check tools, authentication, and state access.
  daemon                     Refresh active providers until interrupted.
  ingest-claude              Read Claude status-line JSON from standard input.
  claude-wrap COMMAND        Preserve an existing Claude status-line command.
  version                    Print version and build metadata.
  help                       Show this help.

Without a command, a terminal opens the dashboard; a pipe or redirect
prints one text status. Dashboard keys: arrows or j/k move, enter opens
details, esc returns, r refreshes, ? shows help, and q quits.
Use R to refresh all active providers.

Claude updates arrive only from active Claude Code status lines.
Antigravity quotas come from agy -p /usage --output-format json.
`)
}
func retryDelay(current, interval time.Duration) time.Duration {
	ceiling := 30 * time.Minute
	if interval > ceiling {
		ceiling = interval
	}
	if current >= ceiling/2 {
		return ceiling
	}
	return current * 2
}

func (a *app) daemonProvider(ctx context.Context, id string, interval time.Duration) {
	delay := interval
	var next time.Time
	wasEnabled := false
	// Poll policy independently of collection so backoff does not delay re-enabling.
	for {
		current, err := newApp()
		enabled := err == nil && current.enabled(id)
		if err != nil {
			fmt.Fprintln(os.Stderr, "aiquota:", err)
		}
		if enabled && (!wasEnabled || !time.Now().Before(next)) {
			err = a.refresh(ctx, id)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errProviderDisabled) {
				enabled = false
			} else if err != nil {
				fmt.Fprintln(os.Stderr, "aiquota:", err)
				delay = retryDelay(delay, interval)
			} else {
				delay = interval
			}
			next = time.Now().Add(delay)
		}
		if !enabled {
			delay = interval
		}
		wasEnabled = enabled
		wait := 5 * time.Second
		if enabled && time.Until(next) < wait {
			wait = time.Until(next)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		help()
		return nil
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("aiquota", version)
		if info, ok := debug.ReadBuildInfo(); ok {
			fmt.Println("Go", info.GoVersion)
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" {
					fmt.Printf("%s: %s\n", setting.Key, setting.Value)
				}
			}
		}
		return nil
	}
	if len(args) > 0 && args[0] == "claude-wrap" {
		if len(args) != 2 {
			return errors.New("claude-wrap requires exactly one existing status-line command")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return claude.RunStatusLine(ctx, os.Stdin, os.Stdout, os.Stderr, args[1], func(r io.Reader) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			return a.ingestClaude(r)
		})
	}
	a, err := newApp()
	if err != nil {
		return err
	}
	defaultCommand := len(args) == 0
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if defaultCommand && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		return a.runTUI(ctx)
	}
	switch cmd {
	case "provider":
		if len(args) != 2 || (args[0] != "enable" && args[0] != "disable") {
			return errors.New("usage: aiquota provider enable|disable claude|codex|antigravity")
		}
		return a.setProvider(args[1], args[0] == "enable")
	case "tui":
		if len(args) != 0 {
			return errors.New("tui accepts no arguments")
		}
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			return errors.New("tui requires an interactive terminal; use aiquota status for text output")
		}
		return a.runTUI(ctx)
	case "status":
		f := flag.NewFlagSet("status", flag.ContinueOnError)
		j := f.Bool("json", false, "output JSON")
		cached := f.Bool("cached", false, "do not refresh")
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected status arguments")
		}
		if !*cached && !a.daemonRunning() {
			if err := a.refresh(ctx, ""); err != nil {
				fmt.Fprintln(os.Stderr, "aiquota:", err)
			}
		}
		o := a.snapshot()
		if *j {
			return json.NewEncoder(os.Stdout).Encode(o)
		}
		a.render(os.Stdout, o)
		return nil
	case "refresh":
		if len(args) > 1 {
			return errors.New("refresh accepts one provider")
		}
		id := ""
		if len(args) == 1 {
			id = args[0]
		}
		if err := a.refresh(ctx, id); err != nil {
			return err
		}
		a.render(os.Stdout, a.snapshot())
		return nil
	case "ingest-claude":
		if len(args) > 0 {
			return errors.New("ingest-claude accepts no arguments")
		}
		return a.ingestClaude(os.Stdin)
	case "doctor":
		if len(args) > 0 {
			return errors.New("doctor accepts no arguments")
		}
		for _, v := range []string{"claude", "codex", "agy", "quickshell"} {
			p, e := exec.LookPath(v)
			if e != nil {
				fmt.Printf("%s: not found\n", v)
			} else {
				fmt.Printf("%s: %s\n", v, p)
			}
		}
		claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
		if claudeDir == "" {
			h, _ := os.UserHomeDir()
			claudeDir = filepath.Join(h, ".claude")
		}
		fmt.Printf("Claude status line: %s\n", claudeStatus(claudeDir))
		gate, current, gateErr := a.providerGate("codex")
		if errors.Is(gateErr, errProviderDisabled) {
			fmt.Println("Codex authentication: skipped (provider disabled)")
		} else if gateErr != nil {
			fmt.Printf("Codex authentication: skipped (%s)\n", gateErr)
		} else {
			fmt.Printf("Codex authentication: %s\n", codexLoginStatus(ctx, current.cfg.CodexPath))
			gate.Close()
		}
		fmt.Printf("State %s: %s\n", a.store.Dir, stateStatus(a.store.Dir))
		return nil
	case "watch":
		if len(args) != 0 {
			return errors.New("watch accepts no arguments")
		}
		a.render(os.Stdout, a.snapshot())
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		last, _ := json.Marshal(a.snapshot())
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				o := a.snapshot()
				b, _ := json.Marshal(o)
				if string(b) != string(last) {
					a.render(os.Stdout, o)
					last = b
				}
			}
		}
	case "daemon":
		if len(args) != 0 {
			return errors.New("daemon accepts no arguments")
		}
		l, err := lock(a.store.Dir, "daemon.lock", true)
		if err != nil {
			return fmt.Errorf("daemon already running or lock unavailable: %w", err)
		}
		defer l.Close()
		var workers sync.WaitGroup
		for _, job := range []struct {
			id       string
			interval time.Duration
		}{{"codex", a.interval}, {"antigravity", a.agyInterval}} {
			workers.Add(1)
			go func(id string, interval time.Duration) {
				defer workers.Done()
				a.daemonProvider(ctx, id, interval)
			}(job.id, job.interval)
		}
		<-ctx.Done()
		workers.Wait()
		return nil

	default:
		return fmt.Errorf("unknown command %q; run aiquota help", cmd)
	}
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitErr *exec.ExitError
		if len(os.Args) > 1 && os.Args[1] == "claude-wrap" && errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "aiquota:", err)
		os.Exit(1)
	}
}
