package claude

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiquota/internal/state"
)

func TestIngest(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := Ingest(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":24.5,"resets_at":1767232800},"seven_day":{"used_percentage":50,"resets_at":1767830400}},"prompt":"secret"}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Windows) != 2 || *p.Windows[0].RemainingPercent != 75.5 {
		t.Fatalf("windows: %+v", p.Windows)
	}
	if p.Windows[0].ResetsAt == nil || p.Windows[0].ResetsAt.Unix() != 1767232800 {
		t.Fatal("bad reset")
	}
}

func TestIngestAbsentAndInvalid(t *testing.T) {
	for _, tc := range []struct {
		input   string
		wantErr bool
	}{{`{}`, false}, {`{"rate_limits":{"five_hour":{"used_percentage":101}}}`, true}, {`{"rate_limits":{"five_hour":{"resets_at":-1}}}`, true}, {`{`, true}, {`{} {}`, true}} {
		_, err := Ingest(strings.NewReader(tc.input), time.Now())
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: %v", tc.input, err)
		}
	}
}

func TestSaveIgnoresOlderAndMissingObservations(t *testing.T) {
	s := state.Store{Dir: filepath.Join(t.TempDir(), "state")}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first, err := Ingest(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":50}}}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if err = Save(s, first); err != nil {
		t.Fatal(err)
	}
	old, err := Ingest(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":90}}}`), now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = Save(s, old); err != nil {
		t.Fatal(err)
	}
	empty, err := Ingest(strings.NewReader(`{}`), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = Save(s, empty); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("claude")
	if err != nil {
		t.Fatal(err)
	}
	if *got.Windows[0].UsedPercent != 50 {
		t.Fatalf("overwrote fresh observation: %+v", got)
	}
}
