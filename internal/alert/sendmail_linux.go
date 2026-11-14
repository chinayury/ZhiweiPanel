//go:build linux

package alert

import (
	"context"
	"io"
	"os/exec"
)

// sendmailCmd 构造调用本机 sendmail 的命令。
func sendmailCmd(ctx context.Context, to string, body io.Reader) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/sbin/sendmail", "-t")
	cmd.Stdin = body
	return cmd
}
