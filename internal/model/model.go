package model

import (
	"fmt"
	"math"
	"time"
)

const SchemaVersion = 1

type Provider struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Status      string     `json:"status"`
	Message     string     `json:"message"`
	Collection  string     `json:"collection"`
	Plan        string     `json:"plan"`
	Credits     *Credits   `json:"credits"`
	Windows     []Window   `json:"windows"`
	ObservedAt  *time.Time `json:"observed_at"`
	LastAttempt *time.Time `json:"last_attempt"`
	LastSuccess *time.Time `json:"last_success"`
}

type Credits struct {
	HasCredits *bool   `json:"has_credits"`
	Unlimited  *bool   `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type Window struct {
	ID               string     `json:"id"`
	BucketID         string     `json:"bucket_id"`
	Label            string     `json:"label"`
	Model            string     `json:"model"`
	DurationMinutes  *int       `json:"duration_minutes"`
	UsedAmount       *float64   `json:"used_amount"`
	RemainingAmount  *float64   `json:"remaining_amount"`
	AllocationLimit  *float64   `json:"allocation_limit"`
	Unit             string     `json:"unit"`
	UsedPercent      *float64   `json:"used_percent"`
	RemainingPercent *float64   `json:"remaining_percent"`
	ResetsAt         *time.Time `json:"resets_at"`
	ObservedAt       *time.Time `json:"observed_at"`
	Source           string     `json:"source"`
}

func NormalizeWindow(w *Window) error {
	if w.UsedPercent != nil {
		n := *w.UsedPercent
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
			return fmt.Errorf("invalid used percentage")
		}
		remaining := 100 - n
		if w.RemainingPercent != nil && (math.IsNaN(*w.RemainingPercent) || math.IsInf(*w.RemainingPercent, 0) || math.Abs(*w.RemainingPercent-remaining) > 1e-9) {
			return fmt.Errorf("inconsistent remaining percentage")
		}
		w.RemainingPercent = &remaining
	}
	if w.RemainingPercent != nil && (math.IsNaN(*w.RemainingPercent) || math.IsInf(*w.RemainingPercent, 0) || *w.RemainingPercent < 0 || *w.RemainingPercent > 100) {
		return fmt.Errorf("invalid remaining percentage")
	}
	for _, amount := range []*float64{w.UsedAmount, w.RemainingAmount, w.AllocationLimit} {
		if amount != nil && (math.IsNaN(*amount) || math.IsInf(*amount, 0) || *amount < 0) {
			return fmt.Errorf("invalid quota amount")
		}
	}
	if w.DurationMinutes != nil && *w.DurationMinutes <= 0 {
		return fmt.Errorf("invalid window duration")
	}
	return nil
}
