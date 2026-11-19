package claude

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

func RunStatusLine(ctx context.Context, input io.Reader, output, stderr io.Writer, command string, ingest func(io.Reader) error) error {
	data, err := io.ReadAll(io.LimitReader(input, maxInput+1))
	if err != nil {
		return err
	}
	complete := len(data) <= maxInput
	var originalErr error
	if command != "" {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		cmd.Stdin = io.MultiReader(bytes.NewReader(data), input)
		cmd.Stdout = output
		cmd.Stderr = stderr
		originalErr = cmd.Run()
	}
	if complete && ingest != nil {
		_ = ingest(bytes.NewReader(data))
	}
	return originalErr
}
