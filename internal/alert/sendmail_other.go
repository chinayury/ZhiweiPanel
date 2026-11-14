//go:build !linux

package alert

import (
	"context"
	"io"
	"os/exec"
)

// sendmailCmd 非 Linux 平台用 sh -c 兜底（开发期不实际发信）。
func sendmailCmd(ctx context.Context, to string, body io.Reader) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", "cat > /dev/null")
	cmd.Stdin = body
	return cmd
}
