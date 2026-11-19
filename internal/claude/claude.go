package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"aiquota/internal/model"
	"aiquota/internal/state"
)

const maxInput = 1 << 20

type window struct {
	Used     *float64 `json:"used_percentage"`
	Reset    *int64   `json:"resets_at"`
	UsedUSD  *float64 `json:"used_usd"`
	LimitUSD *float64 `json:"limit_usd"`
	Period   string   `json:"period"`
}

type input struct {
	RateLimits *struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
		Spend    *window `json:"spend_limit"`
	} `json:"rate_limits"`
}

func Ingest(r io.Reader, observedAt time.Time) (model.Provider, error) {
	p := model.Provider{ID: "claude", Name: "Claude Code", Enabled: true, Collection: "statusline", Status: "unavailable", Message: "Waiting for a Claude Code status-line observation.", Windows: []model.Window{}}
	b, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return p, fmt.Errorf("read status line: %w", err)
	}
	if len(b) > maxInput {
		return p, errors.New("status-line input exceeds 1 MiB")
	}
	var data input
	if err := json.Unmarshal(b, &data); err != nil {
		return p, fmt.Errorf("invalid status-line JSON: %w", err)
	}
	if data.RateLimits == nil {
		return p, nil
	}
	entries := []struct {
		id, label string
		duration  int
		value     *window
	}{{"five_hour", "5 hour", 300, data.RateLimits.FiveHour}, {"seven_day", "7 day", 10080, data.RateLimits.SevenDay}, {"spend_limit", "Spend limit", 0, data.RateLimits.Spend}}
	for _, e := range entries {
		if e.value == nil {
			continue
		}
		w := model.Window{ID: e.id, BucketID: e.id, Label: e.label, UsedPercent: e.value.Used, ObservedAt: &observedAt, Source: "claude_statusline"}
		if e.duration > 0 {
			d := e.duration
			w.DurationMinutes = &d
		}
		if e.value.Reset != nil {
			if *e.value.Reset <= 0 || *e.value.Reset > 253402300799 {
				return p, fmt.Errorf("%s: invalid reset timestamp", e.id)
			}
			t := time.Unix(*e.value.Reset, 0).UTC()
			w.ResetsAt = &t
		}
		if e.id == "spend_limit" {
			w.UsedAmount = e.value.UsedUSD
			w.AllocationLimit = e.value.LimitUSD
			w.Unit = "USD (estimated)"
			if e.value.Period != "" {
				w.Label += " (" + e.value.Period + ")"
			}
		}
		if err := model.NormalizeWindow(&w); err != nil {
			return p, fmt.Errorf("%s: %w", e.id, err)
		}
		if w.UsedPercent != nil || w.UsedAmount != nil || w.AllocationLimit != nil {
			p.Windows = append(p.Windows, w)
		}
	}
	if len(p.Windows) > 0 {
		p.Status = "healthy"
		p.Message = ""
		p.ObservedAt = &observedAt
		p.LastAttempt = &observedAt
		p.LastSuccess = &observedAt
	}
	return p, nil
}

func Save(s state.Store, p model.Provider) error {
	if len(p.Windows) == 0 {
		return nil
	}
	return s.Update("claude", func(old *model.Provider) (model.Provider, error) {
		if old != nil && old.ObservedAt != nil && (p.ObservedAt == nil || old.ObservedAt.After(*p.ObservedAt)) {
			return *old, nil
		}
		return p, nil
	})
}
