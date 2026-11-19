package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"time"

	"aiquota/internal/model"
)

type rateWindow struct {
	UsedPercent *float64 `json:"usedPercent"`
	Duration    *int     `json:"windowDurationMins"`
	ResetsAt    *int64   `json:"resetsAt"`
}
type bucket struct {
	ID      *string `json:"limitId"`
	Name    *string `json:"limitName"`
	Model   *string `json:"normalModelSlug"`
	Plan    *string `json:"planType"`
	Credits *struct {
		HasCredits *bool   `json:"hasCredits"`
		Unlimited  *bool   `json:"unlimited"`
		Balance    *string `json:"balance"`
	} `json:"credits"`
	Primary   *rateWindow `json:"primary"`
	Secondary *rateWindow `json:"secondary"`
}
type limits struct {
	RateLimits *bucket            `json:"rateLimits"`
	Buckets    map[string]*bucket `json:"rateLimitsByLimitId"`
}
type response struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func Read(ctx context.Context, executable string) (model.Provider, error) {
	if executable == "" {
		executable = "codex"
	}
	cmd := exec.CommandContext(ctx, executable, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return model.Provider{}, fmt.Errorf("codex stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return model.Provider{}, fmt.Errorf("codex stdout: %w", err)
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return model.Provider{}, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() {
		stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 4096), 2<<20)
	send := func(v any) error { return json.NewEncoder(stdin).Encode(v) }
	if err = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "aiquota", "title": "AIQuota", "version": "0.1.0"}}}); err != nil {
		return model.Provider{}, fmt.Errorf("initialize codex: %w", err)
	}
	if _, err = receive(scan, 1); err != nil {
		return model.Provider{}, contextError(ctx, err)
	}
	if err = send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return model.Provider{}, fmt.Errorf("acknowledge codex initialization: %w", err)
	}
	if err = send(map[string]any{"id": 2, "method": "account/rateLimits/read"}); err != nil {
		return model.Provider{}, fmt.Errorf("request codex rate limits: %w", err)
	}
	data, err := receive(scan, 2)
	if err != nil {
		return model.Provider{}, contextError(ctx, err)
	}
	return parseLimits(data, time.Now().UTC())
}
func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func receive(scan *bufio.Scanner, id int) (json.RawMessage, error) {
	for scan.Scan() {
		var r response
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			return nil, errors.New("invalid JSON from codex app-server")
		}
		var got int
		if len(r.ID) == 0 || json.Unmarshal(r.ID, &got) != nil || got != id {
			continue
		}
		if r.Error != nil {
			return nil, fmt.Errorf("codex app-server returned error code %d; check login and connectivity", r.Error.Code)
		}
		if len(r.Result) == 0 || string(r.Result) == "null" {
			return nil, errors.New("codex app-server returned an empty response")
		}
		return r.Result, nil
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("read codex app-server: %w", err)
	}
	return nil, errors.New("codex app-server exited before responding")
}
func parseLimits(data []byte, observed time.Time) (model.Provider, error) {
	var result limits
	if err := json.Unmarshal(data, &result); err != nil {
		return model.Provider{}, fmt.Errorf("invalid codex rate limits: %w", err)
	}
	p := model.Provider{ID: "codex", Name: "Codex", Enabled: true, Status: "healthy", Collection: "codex app-server", ObservedAt: &observed, LastSuccess: &observed, LastAttempt: &observed, Windows: []model.Window{}}
	buckets := result.Buckets
	if len(buckets) == 0 && result.RateLimits != nil {
		buckets = map[string]*bucket{"codex": result.RateLimits}
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b := buckets[key]
		if b == nil {
			continue
		}
		id := key
		if b.ID != nil && *b.ID != "" {
			id = *b.ID
		}
		label := id
		if b.Name != nil && *b.Name != "" {
			label = *b.Name
		}
		if id == "codex" && b.Credits != nil {
			p.Credits = &model.Credits{HasCredits: b.Credits.HasCredits, Unlimited: b.Credits.Unlimited, Balance: b.Credits.Balance}
		}
		if b.Plan != nil && p.Plan == "" {
			p.Plan = *b.Plan
		}
		for _, v := range []struct {
			name   string
			window *rateWindow
		}{{"primary", b.Primary}, {"secondary", b.Secondary}} {
			if v.window == nil {
				continue
			}
			w := v.window
			var reset *time.Time
			if w.ResetsAt != nil {
				if *w.ResetsAt <= 0 {
					return model.Provider{}, errors.New("invalid codex reset timestamp")
				}
				t := time.Unix(*w.ResetsAt, 0).UTC()
				if t.Year() > 9999 {
					return model.Provider{}, errors.New("invalid codex reset timestamp")
				}
				reset = &t
			}
			if w.Duration != nil && *w.Duration <= 0 {
				return model.Provider{}, errors.New("invalid codex window duration")
			}
			window := model.Window{ID: id + ":" + v.name, BucketID: id, Label: label + " " + v.name, DurationMinutes: w.Duration, UsedPercent: w.UsedPercent, ResetsAt: reset, ObservedAt: &observed, Source: "codex app-server"}
			if b.Model != nil {
				window.Model = *b.Model
			}
			if err := model.NormalizeWindow(&window); err != nil {
				return model.Provider{}, fmt.Errorf("invalid codex window: %w", err)
			}
			p.Windows = append(p.Windows, window)
		}
	}
	if len(p.Windows) == 0 {
		return model.Provider{}, errors.New("codex returned no quota windows; check ChatGPT account and subscription")
	}
	return p, nil
}
