package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWrapPreservesOriginalOutputAndInput(t *testing.T) {
	input := `{"rate_limits":{"five_hour":{"used_percentage":25}},"session_id":"private"}`
	var output bytes.Buffer
	var ingested string
	err := RunStatusLine(context.Background(), strings.NewReader(input), &output, io.Discard, `cat`, func(r io.Reader) error { b, _ := io.ReadAll(r); ingested = string(b); return nil })
	if err != nil || output.String() != input || ingested != input {
		t.Fatalf("output=%q ingested=%q err=%v", output.String(), ingested, err)
	}
}

func TestWrapIgnoresIngestionErrors(t *testing.T) {
	var output bytes.Buffer
	err := RunStatusLine(context.Background(), strings.NewReader(`{}`), &output, io.Discard, `printf 'unchanged'`, func(io.Reader) error { return errors.New("invalid") })
	if err != nil || output.String() != "unchanged" {
		t.Fatalf("got %q %v", output.String(), err)
	}
}

func TestWrapPassesLargeInputWithoutCollecting(t *testing.T) {
	input := strings.Repeat("a", maxInput+1)
	var output bytes.Buffer
	called := false
	err := RunStatusLine(context.Background(), strings.NewReader(input), &output, io.Discard, `cat`, func(io.Reader) error { called = true; return nil })
	if err != nil || output.String() != input || called {
		t.Fatalf("length=%d called=%v err=%v", output.Len(), called, err)
	}
}

func TestWrapRetainsOriginalFailure(t *testing.T) {
	called := false
	err := RunStatusLine(context.Background(), strings.NewReader(`{}`), io.Discard, io.Discard, `exit 7`, func(io.Reader) error { called = true; return nil })
	if err == nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
