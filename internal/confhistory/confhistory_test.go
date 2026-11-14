package confhistory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConf 在目录里写一个配置文件。
func writeConf(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestSnapshotCapturesOnlyConfFiles 快照只收 .conf，非 .conf 文件必须被忽略。
func TestSnapshotCapturesOnlyConfFiles(t *testing.T) {
	base := t.TempDir()
	conf := filepath.Join(base, "conf.d")
	root := filepath.Join(base, "history")

	writeConf(t, conf, "a.conf", "server { listen 80; }\n")
	writeConf(t, conf, "b.conf", "server { listen 81; }\n")
	writeConf(t, conf, "notes.txt", "不该进快照\n")
	writeConf(t, filepath.Join(conf, "sub"), "c.conf", "子目录里的\n")

	ent, err := Snapshot(root, conf, "site.create", "备注")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(ent.Files) != 2 {
		t.Fatalf("files = %d, want 2 (只收 .conf 且不进子目录)", len(ent.Files))
	}
	// 文件名必须排序，diff 和 UI 展示都依赖稳定顺序
	if ent.Files[0].Name != "a.conf" || ent.Files[1].Name != "b.conf" {
		t.Errorf("文件未按名排序: %s, %s", ent.Files[0].Name, ent.Files[1].Name)
	}
	if ent.Reason != "site.create" || ent.Note != "备注" {
		t.Errorf("reason/note 未落库: %q %q", ent.Reason, ent.Note)
	}
	// SHA256 必须与内容一致
	for _, f := range ent.Files {
		data, err := os.ReadFile(filepath.Join(conf, f.Name))
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		if f.Size != int64(len(data)) {
			t.Errorf("%s size = %d, want %d", f.Name, f.Size, len(data))
		}
		if len(f.SHA256) != 64 {
			t.Errorf("%s sha256 = %q, 期望 64 位十六进制", f.Name, f.SHA256)
		}
	}
	// 快照目录里必须有 manifest
	if _, err := os.Stat(filepath.Join(root, ent.ID, manifestName)); err != nil {
		t.Errorf("manifest 不存在: %v", err)
	}
}

// TestSnapshotMissingConfDir 配置目录不存在时必须报错，不能静默产生空快照。
//
// 空快照会让"回滚"变成"把配置目录清空"，比报错危险得多。
func TestSnapshotMissingConfDir(t *testing.T) {
	base := t.TempDir()
	_, err := Snapshot(filepath.Join(base, "history"), filepath.Join(base, "nope"), "r", "")
	if err == nil {
		t.Fatal("期望报错，实际返回 nil")
	}
}

// TestListNewestFirst List 必须按时间倒序。
func TestListNewestFirst(t *testing.T) {
	base := t.TempDir()
	conf := filepath.Join(base, "conf.d")
	root := filepath.Join(base, "history")
	writeConf(t, conf, "a.conf", "x\n")

	var ids []string
	for i := 0; i < 3; i++ {
		ent, err := Snapshot(root, conf, "r", "")
		if err != nil {
			t.Fatalf("Snapshot %d: %v", i, err)
		}
		ids = append(ids, ent.ID)
		// Windows 时钟精度低（~15ms），连续快照可能拿到同一个时间戳。
		// 睡一小会儿，保证 CreatedAt 严格递增，排序断言才有意义。
		time.Sleep(20 * time.Millisecond)
	}

	list, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list len = %d, want 3", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].CreatedAt.After(list[i-1].CreatedAt) {
			t.Errorf("List 不是倒序: [%d]=%v 早于 [%d]=%v",
				i, list[i].CreatedAt, i-1, list[i-1].CreatedAt)
		}
	}
	// 三份快照都必须能查到
	for _, id := range ids {
		if _, err := Get(root, id); err != nil {
			t.Errorf("Get(%s): %v", id, err)
		}
	}
}

// TestListMissingRoot 快照目录不存在时返回空列表而不是报错。
func TestListMissingRoot(t *testing.T) {
	list, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("List 不该报错: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("len = %d, want 0", len(list))
	}
}

// TestGetRejectsTraversalID 快照 ID 会直接拼进路径，必须挡住穿越。
func TestGetRejectsTraversalID(t *testing.T) {
	base := t.TempDir()
	cases := []string{
		"",
		"../etc",
		"..",
		"a/b",
		"a\\b",
		"a.conf",
		"a b",
		strings.Repeat("a", 65),
	}
	for _, id := range cases {
		if _, err := Get(base, id); err == nil {
			t.Errorf("ID %q 应当被拒绝", id)
		}
	}
}

// TestFileContentRejectsTraversalName 文件名同样要挡穿越。
func TestFileContentRejectsTraversalName(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "history")
	conf := filepath.Join(base, "conf.d")
	writeConf(t, conf, "a.conf", "secret\n")
	ent, err := Snapshot(root, conf, "r", "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// 在快照目录外放一个"机密"文件，穿越读取必须失败
	writeConf(t, base, "secret.conf", "机密\n")

	for _, name := range []string{"", "../secret.conf", "..", "a/b.conf", `a\b.conf`, "../../etc/passwd"} {
		if _, err := FileContent(root, ent.ID, name); err == nil {
			t.Errorf("文件名 %q 应当被拒绝", name)
		}
	}
	// 正常读取必须成功
	data, err := FileContent(root, ent.ID, "a.conf")
	if err != nil {
		t.Fatalf("FileContent: %v", err)
	}
	if string(data) != "secret\n" {
		t.Errorf("内容不对: %q", string(data))
	}
}

// TestRestoreWritesBackAndKeepsExtraFiles 回滚语义的核心约束：
//   - 快照里有的文件被写回；
//   - 当前目录里多出来的文件**必须保留**（多出来的通常是快照之后
//     新建的站点配置，删掉会连带把新站点从 nginx 里摘掉）。
func TestRestoreWritesBackAndKeepsExtraFiles(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "history")
	conf := filepath.Join(base, "conf.d")

	writeConf(t, conf, "a.conf", "旧内容\n")
	writeConf(t, conf, "b.conf", "b 旧内容\n")
	ent, err := Snapshot(root, conf, "r", "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// 现场被改坏：a.conf 内容不对，还多了一个新站点配置
	writeConf(t, conf, "a.conf", "被改坏了\n")
	writeConf(t, conf, "new-site.conf", "新建站点\n")

	restored, err := Restore(root, ent.ID, conf)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 2 {
		t.Errorf("restored = %v, want 2 个文件", restored)
	}

	data, _ := os.ReadFile(filepath.Join(conf, "a.conf"))
	if string(data) != "旧内容\n" {
		t.Errorf("a.conf 未回滚: %q", string(data))
	}
	// 多出来的文件必须还在
	if _, err := os.Stat(filepath.Join(conf, "new-site.conf")); err != nil {
		t.Errorf("新建站点配置被误删: %v", err)
	}
}

// TestRestoreMissingSnapshot 快照不存在时必须报错，不能凭空清空目录。
func TestRestoreMissingSnapshot(t *testing.T) {
	base := t.TempDir()
	conf := filepath.Join(base, "conf.d")
	writeConf(t, conf, "a.conf", "内容\n")

	if _, err := Restore(filepath.Join(base, "history"), "nosuchsnap", conf); err == nil {
		t.Fatal("期望报错")
	}
	// 原有配置不能被动过
	data, _ := os.ReadFile(filepath.Join(conf, "a.conf"))
	if string(data) != "内容\n" {
		t.Errorf("配置被改动了: %q", string(data))
	}
}

// TestPruneKeepsNewest Prune 必须保留最近的 keep 份，删掉更旧的。
func TestPruneKeepsNewest(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "history")
	conf := filepath.Join(base, "conf.d")
	writeConf(t, conf, "a.conf", "x\n")

	for i := 0; i < 5; i++ {
		if _, err := Snapshot(root, conf, "r", ""); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
	}
	if err := Prune(root, 2); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	list, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("保留 %d 份, want 2", len(list))
	}
	// keep<=0 表示不限制
	if err := Prune(root, 0); err != nil {
		t.Fatalf("Prune(0): %v", err)
	}
	list, _ = List(root)
	if len(list) != 2 {
		t.Errorf("keep=0 时不该删除, 剩 %d 份", len(list))
	}
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

// TestDiffIdentical 内容完全一致时必须标 identical，且不产生增删。
func TestDiffIdentical(t *testing.T) {
	d := Diff("a.conf", []byte("l1\nl2\n"), "a.conf", []byte("l1\nl2\n"))
	if !d.Identical {
		t.Error("Identical 应为 true")
	}
	if d.Added != 0 || d.Removed != 0 {
		t.Errorf("Added=%d Removed=%d, want 0/0", d.Added, d.Removed)
	}
}

// TestDiffMiddleChange 只改中间一行时，头尾相同行应保持 " "。
func TestDiffMiddleChange(t *testing.T) {
	old := []byte("l1\nl2\nl3\nl4\nl5\n")
	new := []byte("l1\nl2\nCHANGED\nl4\nl5\n")
	d := Diff("a.conf", old, "a.conf", new)
	if d.Identical {
		t.Error("不该是 identical")
	}
	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("Added=%d Removed=%d, want 1/1", d.Added, d.Removed)
	}
	// 头两行和尾两行是公共部分
	ops := map[string]int{}
	for _, ln := range d.Lines {
		ops[ln.Op]++
	}
	if ops[" "] != 4 {
		t.Errorf("相同行 = %d, want 4", ops[" "])
	}
	// 行号校验：删除行在新文件里没有对应行（NewNo=0），
	// 新增行在老文件里没有对应行（OldNo=0），
	// 所以按 op 分别校验单调性，不能笼统比 NewNo。
	for i := 1; i < len(d.Lines); i++ {
		cur, prev := d.Lines[i], d.Lines[i-1]
		if cur.Op != "-" && prev.Op != "-" && cur.NewNo < prev.NewNo {
			t.Errorf("新行号倒退: %d < %d", cur.NewNo, prev.NewNo)
		}
		if cur.Op != "+" && prev.Op != "+" && cur.OldNo < prev.OldNo {
			t.Errorf("老行号倒退: %d < %d", cur.OldNo, prev.OldNo)
		}
	}
	// 删除行必须没有新行号（0 表示"新文件里不存在这一行"）
	for _, ln := range d.Lines {
		if ln.Op == "-" && ln.NewNo != 0 {
			t.Errorf("删除行不应有新行号: %d", ln.NewNo)
		}
		if ln.Op == "+" && ln.OldNo != 0 {
			t.Errorf("新增行不应有老行号: %d", ln.OldNo)
		}
	}
}

// TestDiffOldMissing 老快照没有这个文件 → 全部算新增。
func TestDiffOldMissing(t *testing.T) {
	d := Diff("", nil, "a.conf", []byte("l1\nl2\n"))
	if !d.OldMissing {
		t.Error("OldMissing 应为 true")
	}
	if d.Added != 2 || d.Removed != 0 {
		t.Errorf("Added=%d Removed=%d, want 2/0", d.Added, d.Removed)
	}
}

// TestDiffNewMissing 当前配置没有这个文件 → 全部算删除。
func TestDiffNewMissing(t *testing.T) {
	d := Diff("a.conf", []byte("l1\nl2\n"), "", nil)
	if !d.NewMissing {
		t.Error("NewMissing 应为 true")
	}
	if d.Removed != 2 || d.Added != 0 {
		t.Errorf("Added=%d Removed=%d, want 0/2", d.Added, d.Removed)
	}
	// 文件名要沿用存在的那一侧
	if d.Name != "a.conf" {
		t.Errorf("Name = %q, want a.conf", d.Name)
	}
}

// TestDiffNoTrailingNewlineBlank 末尾换行不应造出一个假的空新增行。
func TestDiffNoTrailingNewlineBlank(t *testing.T) {
	d := Diff("a.conf", []byte("l1\n"), "a.conf", []byte("l1\n"))
	if d.Added != 0 || d.Removed != 0 {
		t.Errorf("Added=%d Removed=%d, want 0/0（末尾换行不应产生空行差异）", d.Added, d.Removed)
	}
}
