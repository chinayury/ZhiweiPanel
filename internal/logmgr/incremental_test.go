package logmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// 增量采集的核心承诺：同一批日志只计一次；跨午夜按日拆分；
// 截断/改写后不会漏读。这里用真实临时文件覆盖这三条。

func accessLine(ip, date, status, size string) string {
	return fmt.Sprintf(`%s - example.com [%s] "GET / HTTP/1.1" %s %s 0.010 "-" "test-ua" "-"`,
		ip, date, status, size)
}

func writeAppend(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadIncrementalFirstRunAndNoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	writeAppend(t, path,
		accessLine("1.1.1.1", "02/Jan/2026:10:00:00 +0800", "200", "100"),
		accessLine("2.2.2.2", "02/Jan/2026:10:01:00 +0800", "404", "50"),
	)

	// 首次采集：从文件末尾开始，不把历史日志重算一遍。
	batch, err := ReadIncremental(path, "site1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.StartedAtEnd {
		t.Fatal("首次采集应从文件末尾开始")
	}
	if len(batch.Days) != 0 {
		t.Fatalf("首次采集不应产生统计，得到 %d 天", len(batch.Days))
	}
	if batch.Checkpoint.Offset <= 0 || batch.Checkpoint.Sample == "" {
		t.Fatalf("checkpoint 不完整: %+v", batch.Checkpoint)
	}

	// 追加一条：只计这一条。
	writeAppend(t, path, accessLine("3.3.3.3", "02/Jan/2026:10:02:00 +0800", "500", "20"))
	batch, err = ReadIncremental(path, "site1", &batch.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Days) != 1 {
		t.Fatalf("应聚合出 1 天，得到 %d", len(batch.Days))
	}
	day := batch.Days[0]
	if day.Stat.Requests != 1 || day.Stat.Bytes != 20 || day.Stat.Status5xx != 1 {
		t.Fatalf("增量统计错误: %+v", day.Stat)
	}
	if day.StatusCodes[500] != 1 {
		t.Fatalf("状态码明细错误: %+v", day.StatusCodes)
	}

	// 再采一次：没有新增，必须为 0（旧实现在这里会重复累加）。
	batch, err = ReadIncremental(path, "site1", &batch.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Days) != 0 {
		t.Fatalf("无新增时应为 0 天，得到 %d", len(batch.Days))
	}
}

func TestReadIncrementalSplitsByDateAndDedupIPs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	writeAppend(t, path, accessLine("9.9.9.9", "02/Jan/2026:23:59:00 +0800", "200", "10"))
	first, err := ReadIncremental(path, "site1", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeAppend(t, path,
		accessLine("1.1.1.1", "03/Jan/2026:00:01:00 +0800", "200", "10"),
		accessLine("1.1.1.1", "03/Jan/2026:00:02:00 +0800", "301", "10"),
		accessLine("2.2.2.2", "03/Jan/2026:00:03:00 +0800", "200", "10"),
	)
	batch, err := ReadIncremental(path, "site1", &first.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Days) != 1 || batch.Days[0].Stat.Date != "2026-01-03" {
		t.Fatalf("跨午夜应只聚合新的一天，得到 %+v", batch.Days)
	}
	d := batch.Days[0]
	if d.Stat.Requests != 3 || d.Stat.Status2xx != 2 || d.Stat.Status3xx != 1 {
		t.Fatalf("按日统计错误: %+v", d.Stat)
	}
	if len(d.IPs) != 2 {
		t.Fatalf("同日 IP 应去重为 2，得到 %v", d.IPs)
	}

	// 把两天都读一遍（模拟新站点从 0 开始），验证真正跨日拆分。
	fromStart := &model.TrafficLogOffset{SiteID: "site1", Path: path, Offset: 0}
	full, err := ReadIncremental(path, "site1", fromStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Days) != 2 {
		t.Fatalf("应拆成 2 天，得到 %d", len(full.Days))
	}
}

func TestReadIncrementalDetectsSameSizeRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	writeAppend(t, path, accessLine("1.1.1.1", "02/Jan/2026:10:00:00 +0800", "200", "100"))
	first, err := ReadIncremental(path, "site1", nil)
	if err != nil {
		t.Fatal(err)
	}
	// copytruncate 后又长回相同大小：size 与 inode 都不变，只能靠采样识别。
	old := accessLine("1.1.1.1", "02/Jan/2026:10:00:00 +0800", "200", "100")
	newLine := accessLine("2.2.2.2", "02/Jan/2026:10:00:00 +0800", "404", "100")
	if len(old) != len(newLine) {
		t.Fatalf("测试数据长度需一致: %d vs %d", len(old), len(newLine))
	}
	if err := os.WriteFile(path, []byte(newLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadIncremental(path, "site1", &first.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Days) != 1 || batch.Days[0].Stat.Status4xx != 1 {
		t.Fatalf("同大小改写应被采样识别并从 0 重读，得到 %+v", batch.Days)
	}
}

func TestReadIncrementalSkipsLongLineOnlyWhenComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	longLine := strings.Repeat("x", maxLogLineBytes+1)
	if err := os.WriteFile(path, []byte(longLine), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadIncremental(path, "site1", &model.TrafficLogOffset{})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Checkpoint.Offset != 0 {
		t.Fatal("incomplete line advanced the checkpoint")
	}
	valid := accessLine("1.1.1.1", "02/Jan/2026:10:00:00 +0800", "200", "10")
	writeAppend(t, path, "", valid)
	batch, err = ReadIncremental(path, "site1", &batch.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Checkpoint.Offset != int64(len(longLine)+len(valid)+2) || len(batch.Days) != 1 || batch.Days[0].Stat.Requests != 1 {
		t.Fatal("expected the complete log to be consumed and one request counted")
	}
}
