// Package confhistory 保存 nginx 配置的历史快照，并支持回滚。
//
// 为什么需要（借鉴 1Panel 的 snapshot 表与 cenvero-fleet 的配置漂移检测后得出）：
//
// ZhiweiPanel 的 ApplyAllConfig 会**整份覆盖** conf.d 下的站点配置文件。
// 如果这次覆盖是错的（模板改错、字段填错、端口冲突），
// 运维手上没有任何"上一版好配置"的入口——只能凭记忆手改回去。
// 站点一多、模板一改，这个风险就从"偶尔"变成"迟早"。
//
// 设计：
//   - 每次配置下发成功后，把整个 conf.d 目录复制成一份快照；
//   - 快照存盘（不存 DB）——配置文件可能有几十 KB，放进 SQLite
//     会让主库体积和备份时间一起膨胀；
//   - 保留最近 N 份，旧的自动清理（配置历史不是审计档案，
//     要长期留痕请用审计日志）；
//   - 回滚 = 把快照里的文件写回 conf.d，再重载 nginx。
package confhistory

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileMeta 描述快照里的一个文件。
type FileMeta struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Entry 是一次配置快照。
type Entry struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// Reason 说明这次快照的触发原因，如 site.create / config.apply。
	Reason string     `json:"reason"`
	Files  []FileMeta `json:"files"`
	Bytes  int64      `json:"bytes"`
	// Note 是快照时的自由备注（可空）。
	Note string `json:"note"`
}

// manifest 是快照目录里的元数据文件。
type manifest struct {
	Entry
}

// manifestName 快照目录内的清单文件名。
const manifestName = "manifest.json"

// maxSnapshotBytes 单个快照允许的总大小上限。
//
// nginx 配置正常只有几 KB，但用户可以在 RawConf 里塞任意内容。
// 不设上限的话，一个 100MB 的 RawConf 能把磁盘迅速吃满。
const maxSnapshotBytes = 32 << 20 // 32MB

// Snapshot 把 confDir 下的所有 *.conf 复制成一份快照。
//
// 返回的 Entry.ID 是快照目录名。confDir 不存在时返回错误
// （没目录说明面板数据目录被删了，这是异常状态，不该静默跳过）。
func Snapshot(root, confDir, reason, note string) (*Entry, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(confDir)
	if err != nil {
		return nil, fmt.Errorf("读取配置目录失败: %w", err)
	}

	var metas []FileMeta
	var total int64
	// 先扫一遍算总量，超过上限直接拒绝——拷贝到一半再删是更糟的做法。
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		if total > maxSnapshotBytes {
			return nil, fmt.Errorf("配置总量 %d 字节超过快照上限 %d 字节", total, maxSnapshotBytes)
		}
	}

	now := time.Now()
	id := now.UTC().Format("20060102T150405") + "-" + randomSuffix()
	dst := filepath.Join(root, id)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		src := filepath.Join(confDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o640); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		metas = append(metas, FileMeta{
			Name:   e.Name(),
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })

	ent := Entry{
		ID:        id,
		CreatedAt: now,
		Reason:    reason,
		Files:     metas,
		Bytes:     total,
		Note:      note,
	}
	mb, err := json.MarshalIndent(manifest{Entry: ent}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dst, manifestName), mb, 0o640); err != nil {
		return nil, err
	}
	return &ent, nil
}

// List 列出所有快照，按时间倒序（新的在前）。
func List(root string) ([]Entry, error) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		ent, err := readManifest(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		out = append(out, ent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// readManifest 读取快照清单。
func readManifest(dir string) (Entry, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return Entry{}, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Entry{}, err
	}
	return m.Entry, nil
}

// Get 读取指定快照的元数据。
func Get(root, id string) (Entry, error) {
	if err := validateID(id); err != nil {
		return Entry{}, err
	}
	return readManifest(filepath.Join(root, id))
}

// FileContent 读取快照里某个文件的原始内容。
func FileContent(root, id, name string) ([]byte, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	// 文件名也要校验：../ 穿越会让读取落到快照目录之外。
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("非法文件名")
	}
	return os.ReadFile(filepath.Join(root, id, name))
}

// Restore 把快照里的配置写回 confDir。
//
// 只恢复快照里存在的文件；confDir 里多出来的文件不动
// （多出来的通常是后来新建的站点配置，回滚旧快照时删掉它们
// 会连带删站点——这个后果比"残留一个文件"严重得多）。
//
// 返回实际恢复的文件名列表。
func Restore(root, id, confDir string) ([]string, error) {
	ent, err := Get(root, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return nil, err
	}
	var restored []string
	for _, f := range ent.Files {
		data, err := FileContent(root, id, f.Name)
		if err != nil {
			return nil, fmt.Errorf("读取快照文件 %s 失败: %w", f.Name, err)
		}
		if err := os.WriteFile(filepath.Join(confDir, f.Name), data, 0o644); err != nil {
			return nil, fmt.Errorf("写回 %s 失败: %w", f.Name, err)
		}
		restored = append(restored, f.Name)
	}
	return restored, nil
}

// Prune 保留最近 keep 份快照，删除更旧的。
//
// keep<=0 表示不限制。
func Prune(root string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := List(root)
	if err != nil {
		return err
	}
	if len(entries) <= keep {
		return nil
	}
	for _, e := range entries[keep:] {
		if err := os.RemoveAll(filepath.Join(root, e.ID)); err != nil {
			return err
		}
	}
	return nil
}

// validateID 校验快照 ID。
//
// ID 会直接参与路径拼接，必须严格限制——否则 "../.." 可以读到
// 快照目录之外的文件。
func validateID(id string) error {
	if id == "" || len(id) > 64 {
		return fmt.Errorf("非法快照 ID")
	}
	for _, r := range id {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-'
		if !ok {
			return fmt.Errorf("非法快照 ID")
		}
	}
	return nil
}

// randomSuffix 生成 8 位十六进制随机后缀，避免同一秒内多次快照撞目录名。
//
// 早期实现用 time.Now().UnixNano() % 10000 做后缀，注释写的是"区分同一秒内的
// 两次快照"。但这不成立：macOS 的时钟精度是微秒级，纳秒尾数恒为 1000 的倍数，
// 于是相隔 20ms 的两次快照 ns%10000 完全相等 → 目录名撞车 → 后一份快照覆盖
// 前一份（CI 在 macOS 上稳定复现 "list len = 2, want 3"）。
//
// 后缀不承载任何安全语义，但必须是**真随机**才能保证唯一。
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 不可用极罕见；退回纳秒尾数，至少还能产出 ID。
		return strconv.FormatInt(time.Now().UnixNano()%10000, 10)
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// 差异对比
// ---------------------------------------------------------------------------

// DiffLine 是一行差异。
type DiffLine struct {
	Op   string `json:"op"` // " " 相同 / "-" 删除 / "+" 新增
	Text string `json:"text"`
	// OldNo/NewNo 是原文件/新文件的行号（1 起），相同行两者相同。
	OldNo int `json:"old_no"`
	NewNo int `json:"new_no"`
}

// FileDiff 是两个文件的差异。
type FileDiff struct {
	Name  string     `json:"name"`
	Lines []DiffLine `json:"lines"`
	// Added/Removed/Deleted 统计。
	Added   int `json:"added"`
	Removed int `json:"removed"`
	// Identical 为 true 表示两个文件完全一致。
	Identical bool `json:"identical"`
	// OldMissing/NewMissing 表示其中一侧不存在该文件。
	OldMissing bool `json:"old_missing"`
	NewMissing bool `json:"new_missing"`
}

// Diff 比较两个文件的内容。
//
// 实现说明：先剥掉公共前缀与公共后缀，只对中间真正不同的部分做比较。
// nginx 配置文件通常几百行，LCS 的 O(n*m) 在极端情况下会到千万级运算，
// 而"剥头去尾"在配置文件的实际变更场景下几乎总能命中——
// 改一个 location 块时，前后几百行都是相同的。
//
// oldName 为空表示"原先没有这个文件"，newName 为空表示"现在没有这个文件"。
func Diff(oldName string, oldData []byte, newName string, newData []byte) FileDiff {
	d := FileDiff{Name: newName}
	if newName == "" {
		d.Name = oldName
		d.NewMissing = true
	}
	if oldName == "" {
		d.OldMissing = true
	}
	if oldData == nil && newData == nil {
		d.Identical = true
		return d
	}

	oldLines := splitLines(oldData)
	newLines := splitLines(newData)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	// 相同的头部
	for i := 0; i < prefix; i++ {
		d.Lines = append(d.Lines, DiffLine{Op: " ", Text: oldLines[i], OldNo: i + 1, NewNo: i + 1})
	}
	// 删除的中间段
	for i := prefix; i < len(oldLines)-suffix; i++ {
		d.Lines = append(d.Lines, DiffLine{Op: "-", Text: oldLines[i], OldNo: i + 1})
		d.Removed++
	}
	// 新增的中间段
	for i := prefix; i < len(newLines)-suffix; i++ {
		d.Lines = append(d.Lines, DiffLine{Op: "+", Text: newLines[i], NewNo: i + 1})
		d.Added++
	}
	// 相同的尾部
	oldBase := len(oldLines) - suffix
	newBase := len(newLines) - suffix
	for i := 0; i < suffix; i++ {
		d.Lines = append(d.Lines, DiffLine{
			Op:    " ",
			Text:  oldLines[oldBase+i],
			OldNo: oldBase + i + 1,
			NewNo: newBase + i + 1,
		})
	}

	d.Identical = d.Added == 0 && d.Removed == 0
	return d
}

// splitLines 把字节切成行，保留空行。
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	// 末尾换行会造出一个空元素，去掉它以免 diff 里出现假的新增空行。
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}
