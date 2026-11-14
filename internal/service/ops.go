package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zhiwei/panel/internal/confhistory"
	"github.com/zhiwei/panel/internal/sysinfo"
)

// ---------------------------------------------------------------------------
// 系统运行指标
// ---------------------------------------------------------------------------

// Metrics 返回最近一次采样的宿主机指标。
func (s *Service) Metrics() sysinfo.Snapshot {
	return s.metrics.Snapshot()
}

// MetricsHistory 返回最近的历史采样（旧的在前）。
func (s *Service) MetricsHistory() []sysinfo.Snapshot {
	return s.metrics.History()
}

// StartMetricsCollector 启动后台指标采集。
//
// 采样间隔 10 秒：太短会白白吃 CPU，也会把 /proc/stat 的 jiffies 差值
// 压到噪声范围；太长则曲线太粗，看不出毛刺。
//
// **必须放后台 goroutine**：sysinfo.Collector.Start 是死循环（设计如此，
// 和 http.Server.ListenAndServe 同型），同步调用会把 main() 卡死在这里，
// HTTP 服务永远起不来——进程活着但不监听任何端口，从外面看就是"面板挂了"。
// v0.2.2 首次部署就是这么炸的：进程在、端口不在、日志停在"已保存配置快照"。
// 参照 internal/cron 的 Scheduler.Start：同步做初始化，循环用 go 拉起。
func (s *Service) StartMetricsCollector(ctx context.Context) {
	go s.metrics.Start(ctx, 10*time.Second)
}

// ---------------------------------------------------------------------------
// nginx 配置历史
//
// 借鉴 1Panel 的 snapshot 表与 cenvero-fleet 的配置漂移检测：
// 每次配置下发成功后留一份快照，出问题能回到已知可用的一版。
// ---------------------------------------------------------------------------

// configHistoryKeep 是保留的快照份数。
const configHistoryKeep = 30

// configHistoryRoot 返回配置历史根目录。
func (s *Service) configHistoryRoot() string {
	return filepath.Join(s.dataDir, "conf_history")
}

// SnapshotConfig 保存一份当前 nginx 配置快照。
//
// 失败只记日志、不影响主流程——快照是辅助设施，
// 不能因为磁盘满导致用户的建站操作失败。
func (s *Service) SnapshotConfig(reason, note string) {
	root := s.configHistoryRoot()
	confDir := s.drv.Paths().SiteConfDir
	entry, err := confhistory.Snapshot(root, confDir, reason, note)
	if err != nil {
		log.Printf("[confhistory] 保存配置快照失败: %v", err)
		return
	}
	log.Printf("[confhistory] 已保存配置快照 %s（%d 个文件，%d 字节）", entry.ID, len(entry.Files), entry.Bytes)
	if err := confhistory.Prune(root, configHistoryKeep); err != nil {
		log.Printf("[confhistory] 清理旧快照失败: %v", err)
	}
}

// ListConfigHistory 列出配置快照（新的在前）。
func (s *Service) ListConfigHistory() ([]confhistory.Entry, error) {
	return confhistory.List(s.configHistoryRoot())
}

// GetConfigSnapshot 读取一份快照的元数据与文件内容。
func (s *Service) GetConfigSnapshot(id string) (confhistory.Entry, map[string]string, error) {
	entry, err := confhistory.Get(s.configHistoryRoot(), id)
	if err != nil {
		return confhistory.Entry{}, nil, err
	}
	contents := make(map[string]string, len(entry.Files))
	for _, f := range entry.Files {
		data, err := confhistory.FileContent(s.configHistoryRoot(), id, f.Name)
		if err != nil {
			continue
		}
		contents[f.Name] = string(data)
	}
	return entry, contents, nil
}

// DiffConfigSnapshot 把快照与当前生效配置逐文件对比。
//
// 快照里有但现在没有的文件（站点被删了）标 OldMissing；
// 现在有但快照里没有的文件（快照之后新建的站点）标 NewMissing。
func (s *Service) DiffConfigSnapshot(id string) ([]confhistory.FileDiff, error) {
	root := s.configHistoryRoot()
	entry, err := confhistory.Get(root, id)
	if err != nil {
		return nil, err
	}
	confDir := s.drv.Paths().SiteConfDir

	// 当前实际存在的配置文件
	cur := make(map[string][]byte)
	if entries, err := os.ReadDir(confDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".conf" {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(confDir, e.Name())); err == nil {
				cur[e.Name()] = data
			}
		}
	}

	snapNames := make(map[string]bool, len(entry.Files))
	for _, f := range entry.Files {
		snapNames[f.Name] = true
	}

	var diffs []confhistory.FileDiff
	seen := make(map[string]bool)

	// 快照里的文件
	for _, f := range entry.Files {
		seen[f.Name] = true
		oldData, err := confhistory.FileContent(root, id, f.Name)
		if err != nil {
			continue
		}
		newData, ok := cur[f.Name]
		if !ok {
			d := confhistory.Diff(f.Name, oldData, f.Name, nil)
			d.NewMissing = true
			d.OldMissing = false
			diffs = append(diffs, d)
			continue
		}
		diffs = append(diffs, confhistory.Diff(f.Name, oldData, f.Name, newData))
	}
	// 当前有、快照里没有的文件
	curNames := make([]string, 0, len(cur))
	for n := range cur {
		curNames = append(curNames, n)
	}
	sort.Strings(curNames)
	for _, n := range curNames {
		if seen[n] {
			continue
		}
		d := confhistory.Diff("", nil, n, cur[n])
		d.OldMissing = true
		d.NewMissing = false
		diffs = append(diffs, d)
	}

	return diffs, nil
}

// RestoreConfigSnapshot 把配置回滚到指定快照并重载 nginx。
//
// 只恢复快照里存在的文件；当前目录里多出来的文件不动。
// 理由：多出来的通常是快照之后新建的站点配置，
// 回滚旧快照时顺手删掉会连带把新站点从 nginx 里摘掉——
// 那个后果比"残留一个旧配置文件"严重得多。
func (s *Service) RestoreConfigSnapshot(ctx context.Context, id string) ([]string, error) {
	root := s.configHistoryRoot()
	confDir := s.drv.Paths().SiteConfDir

	// 先备份当前内容——nginx 检测失败时要能原样退回去。
	// 不能只删文件了事：那会把用户手写的 conf 之外的东西也弄丢。
	prev, err := s.captureConfFiles(confDir)
	if err != nil {
		return nil, err
	}

	restored, err := confhistory.Restore(root, id, confDir)
	if err != nil {
		return nil, err
	}
	if len(restored) == 0 {
		return nil, fmt.Errorf("快照 %s 里没有可恢复的配置文件", id)
	}

	// 校验 + 重载。校验不过就把文件恢复回去，不能留一份 nginx 读不了的配置。
	if ok, out, err := s.drv.NginxTest(ctx); err != nil || !ok {
		s.restoreConfFiles(confDir, prev)
		if err != nil {
			return nil, fmt.Errorf("回滚后 nginx 配置检测失败（已撤销回滚）: %w", err)
		}
		return nil, fmt.Errorf("回滚后 nginx 配置检测失败（已撤销回滚）: %s", out)
	}
	if err := s.drv.Reload(ctx); err != nil {
		return nil, fmt.Errorf("回滚已写入但 nginx 重载失败: %w", err)
	}

	// 回滚本身也是一次状态变化，留一份快照以便再回滚回去。
	s.SnapshotConfig("config.restore", "由快照 "+id+" 回滚")
	return restored, nil
}

// confFileBackup 是一个配置文件的原内容备份。
type confFileBackup struct {
	name    string
	content []byte
	existed bool
}

// captureConfFiles 备份指定目录下所有 .conf 的当前内容。
func (s *Service) captureConfFiles(confDir string) (map[string]confFileBackup, error) {
	out := make(map[string]confFileBackup)
	entries, err := os.ReadDir(confDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".conf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(confDir, e.Name()))
		if err != nil {
			continue
		}
		out[e.Name()] = confFileBackup{name: e.Name(), content: data, existed: true}
	}
	return out, nil
}

// restoreConfFiles 把配置目录恢复到备份时的状态。
//
// 回滚前不存在的文件会被删掉（它是我们刚写进去的）。
func (s *Service) restoreConfFiles(confDir string, prev map[string]confFileBackup) {
	entries, err := os.ReadDir(confDir)
	if err != nil {
		return
	}
	cur := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".conf" {
			continue
		}
		cur[e.Name()] = true
	}
	for name := range cur {
		if _, ok := prev[name]; !ok {
			_ = os.Remove(filepath.Join(confDir, name))
		}
	}
	for name, b := range prev {
		if err := os.WriteFile(filepath.Join(confDir, name), b.content, 0o644); err != nil {
			log.Printf("[confhistory] 撤销回滚时写回 %s 失败: %v", name, err)
		}
	}
}
