package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBuckets(t *testing.T) {
	data := []byte(`{"rateLimits":{"limitId":"legacy","primary":{"usedPercent":2}},"rateLimitsByLimitId":{"z":{"limitId":"z","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1900000000}},"a":{"limitName":"Other","secondary":{"usedPercent":40,"windowDurationMins":60}}}}`)
	p, err := parseLimits(data, time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Windows) != 2 || p.Windows[0].BucketID != "a" || p.Windows[1].BucketID != "z" || *p.Windows[1].RemainingPercent != 75 {
		t.Fatalf("unexpected windows: %+v", p.Windows)
	}
}

func TestParseInvalidPercent(t *testing.T) {
	_, err := parseLimits([]byte(`{"rateLimits":{"primary":{"usedPercent":101}}}`), time.Now())
	if err == nil {
		t.Fatal("invalid percentage accepted")
	}
}

func TestReadProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
read init
case "$init" in *'"method":"initialize"'*) ;; *) exit 1;; esac
printf '%s\n' '{"method":"notification"}' '{"id":99,"result":{}}' '{"id":1,"result":{}}'
read initialized
case "$initialized" in *'"method":"initialized"'*) ;; *) exit 1;; esac
read request
case "$request" in *'"method":"account/rateLimits/read"'*) ;; *) exit 1;; esac
printf '%s\n' '{"id":2,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":20}}}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	p, err := Read(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Windows) != 1 || *p.Windows[0].RemainingPercent != 80 {
		t.Fatalf("unexpected windows: %+v", p.Windows)
	}
}

func TestReadMalformedAndCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nread x\nprintf 'not-json\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := Read(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Read(ctx, path)
	if err == nil {
		t.Fatal("cancellation accepted")
	}
}

func TestReadServerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nread init\nprintf '%s\\n' '{\"id\":1,\"error\":{\"code\":-32000,\"message\":\"secret\"}}'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := Read(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "-32000") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Read(ctx, path)
	if err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyLimits(t *testing.T) {
	if _, err := parseLimits([]byte(`{"rateLimits":null}`), time.Now()); err == nil {
		t.Fatal("empty result accepted")
	}
}
