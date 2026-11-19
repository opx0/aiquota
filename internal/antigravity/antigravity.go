package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"time"

	"aiquota/internal/model"
)

const maxOutput = 1 << 20

type bucket struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Window    string   `json:"window"`
	Remaining *float64 `json:"remaining_fraction"`
	Reset     *string  `json:"reset_time"`
	Disabled  bool     `json:"disabled"`
}
type usage struct {
	Status   string `json:"status"`
	NumTurns *int   `json:"num_turns"`
	Command  *struct {
		Name string `json:"name"`
		Data struct {
			Groups []struct {
				Name    string   `json:"name"`
				Buckets []bucket `json:"buckets"`
			} `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutput {
		return 0, errors.New("agy output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

func Read(ctx context.Context, executable string) (model.Provider, error) {
	if executable == "" {
		executable = "agy"
	}
	cmd := exec.CommandContext(ctx, executable, "-p", "/usage", "--output-format", "json")
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return model.Provider{}, ctx.Err()
		}
		return model.Provider{}, fmt.Errorf("agy usage command failed: %w", err)
	}
	return parse(out.Bytes(), time.Now().UTC())
}
func parse(data []byte, now time.Time) (model.Provider, error) {
	var result usage
	if err := json.Unmarshal(data, &result); err != nil {
		return model.Provider{}, errors.New("invalid agy usage JSON")
	}
	if result.Status != "SUCCESS" || result.NumTurns == nil || *result.NumTurns != 0 || result.Command == nil || result.Command.Name != "usage" {
		return model.Provider{}, errors.New("agy did not return a usage response")
	}
	p := model.Provider{ID: "antigravity", Name: "Antigravity", Enabled: true, Status: "healthy", Collection: "agy usage", ObservedAt: &now, LastAttempt: &now, LastSuccess: &now, Windows: []model.Window{}}
	seen := map[string]bool{}
	for _, group := range result.Command.Data.Groups {
		for _, b := range group.Buckets {
			if b.Disabled {
				continue
			}
			if b.ID == "" || seen[b.ID] {
				return model.Provider{}, errors.New("agy returned an empty or duplicate bucket ID")
			}
			seen[b.ID] = true
			w := model.Window{ID: b.ID, BucketID: b.ID, Label: b.Name, Model: group.Name, ObservedAt: &now, Source: "agy usage"}
			if w.Label == "" {
				w.Label = b.Window
			}
			if w.Label == "" {
				w.Label = b.ID
			}
			switch b.Window {
			case "5h":
				n := 300
				w.DurationMinutes = &n
			case "weekly":
				n := 10080
				w.DurationMinutes = &n
			}
			if b.Remaining != nil {
				if math.IsNaN(*b.Remaining) || math.IsInf(*b.Remaining, 0) || *b.Remaining < 0 || *b.Remaining > 1 {
					return model.Provider{}, errors.New("invalid agy remaining fraction")
				}
				used := (1 - *b.Remaining) * 100
				w.UsedPercent = &used
			}
			if b.Reset != nil && *b.Reset != "" {
				t, err := time.Parse(time.RFC3339, *b.Reset)
				if err != nil {
					return model.Provider{}, errors.New("invalid agy reset timestamp")
				}
				t = t.UTC()
				w.ResetsAt = &t
			}
			if err := model.NormalizeWindow(&w); err != nil {
				return model.Provider{}, fmt.Errorf("invalid agy window: %w", err)
			}
			p.Windows = append(p.Windows, w)
		}
	}
	if len(p.Windows) == 0 {
		return model.Provider{}, errors.New("agy returned no quota windows")
	}
	return p, nil
}
