package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseUsage(t *testing.T) {
	data := []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0.75,"reset_time":"2026-11-15T00:00:00Z"},{"id":"gemini-5h","window":"5h","remaining_fraction":1,"reset_time":null}]},{"name":"Claude and GPT models","buckets":[{"id":"3p-5h","window":"5h","remaining_fraction":0.5}]}]}}}`)
	p, err := parse(data, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Windows) != 3 || p.Windows[0].BucketID != "gemini-weekly" || *p.Windows[0].RemainingPercent != 75 || *p.Windows[0].UsedPercent != 25 || *p.Windows[0].DurationMinutes != 10080 || p.Windows[0].ResetsAt == nil {
		t.Fatalf("unexpected result: %+v", p.Windows)
	}
}
func TestInvalidUsage(t *testing.T) {
	cases := []struct{ data, want string }{
		{`{}`, "did not return a usage response"},
		{`{"status":"SUCCESS","num_turns":1}`, "did not return a usage response"},
		{`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"name":"X","buckets":[{"id":"a","remaining_fraction":1.1}]}]}}}`, "invalid agy remaining fraction"},
		{`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"a","remaining_fraction":0.5,"reset_time":"invalid"}]}]}}}`, "invalid agy reset timestamp"},
	}
	for _, tc := range cases {
		_, err := parse([]byte(tc.data), time.Now())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("got %v, want %q", err, tc.want)
		}
	}
}
func TestReadCommandAndTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agy")
	script := `#!/bin/sh
[ "$1" = "-p" ] && [ "$2" = "/usage" ] && [ "$3" = "--output-format" ] && [ "$4" = "json" ] || exit 1
printf '%s' '{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"name":"Gemini","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.5}]}]}}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := Read(context.Background(), path)
	if err != nil || len(p.Windows) != 1 {
		t.Fatalf("got %+v %v", p, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Read(ctx, path); err == nil {
		t.Fatal("timeout accepted")
	}
}
