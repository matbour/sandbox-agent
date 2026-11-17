package sbx

import (
	"bytes"
	"context"
	"os/exec"
)

func run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sbx", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	return cmd.CombinedOutput()
}
