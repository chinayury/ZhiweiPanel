// Package version 是 ZhiweiPanel 版本号的**唯一权威来源**。
//
// 为什么单独一个包：版本号会散落在很多地方——
// main.go、Dockerfile、docker-compose.yml、install.sh、README、各份文档……
// 早期版本这些地方各写各的，结果是"代码里是 0.2.2、部署文件里是 0.1.0"，
// 面板上显示的版本、实际部署的二进制、文档写的版本三者对不上。
//
// 所以这里定规矩：
//
//  1. **唯一权威源**：本包的 Version 常量。其它任何地方都不得再写死版本号。
//  2. **全锚点对齐**：所有锚点（构建、部署、文档）都必须与本包一致，
//     由 version_test.go 这道门禁在每次 `go test ./...` 时强制检查。
//  3. **历史标记单独登记**：已经发布过的历史版本号登记在 History 里，
//     门禁扫描时按语境放行（CHANGELOG、版本对比文档），
//     不登记的版本号一律视为"漂移"而失败。
//
// 门禁是**常驻**的：它是一个 Go 测试，跟着 `go test ./...` 一起跑，
//
//	CI 与本地都不会漏。
package version

// Version 当前版本号。
//
// v0.1.0 = 正式稳定版：把此前线上运行的 v0.2.x 全部功能集正式化、
// 完成全量审计、修复与安全加固后的首个"稳定版"承诺。
// v0.1.1 = 改名：SitePanel → 智维面板 / ZhiweiPanel。
// v0.1.2 = 修复未绑定证书的站点在详情页永远无法保存的故障
// （前端 mergeDefaults 回填空时间字符串 cert → 后端 time.Time
// 解码失败 → 每次 PUT 400；前后端双修复）。
// v0.1.3 = 修复整站反代时 force_https 的 301 跳转被静默丢弃
// （buildLocation 提前返回，跳转指令无处可写；改为补写进接管的
// location ^~ / 内）。
// v0.1.4 = 项目公开后的首个带 Tag 与 Release 产物的版本：修复 Docker
// 部署开箱即用（镜像入口拉起 nginx、conf.d include 移入 http 块、
// 删除发行版默认站点、pid 探测兼容 alpine）、CI 修复（Go 版本对齐、
// CRLF 归一、快照 ID 真随机），并新增 tag 触发的发布流水线。
// v0.1.5 = 文档与交付加固：README 已知限制勘误（#14/#15 描述的 UI
// 断点经实机核实早已实现，属文档漂移，移除后 16 条 → 14 条）、
// 新增面向 AI agent 的 docs/AGENT.md、CI Docker job 增加 nginx 存活断言。
// v0.3.0 = 新增用户可见功能：审计日志导出（CSV / JSON，导出本身留痕
// audit.export；CSV 带 UTF-8 BOM 兼容 Excel），运维中心「操作审计」页
// 增加导出按钮。**版本号从 0.1.5 直接跳 0.3.0**：minor 应到 0.2.0，但
// 0.2.0 ~ 0.2.2 已被版本线重置前的旧线占用（见下方 History），
// 复用会造成 CHANGELOG 出现两个语义不同的 v0.2.0，故跳号并在此说明。
//
// ⚠️ 改版本号时只改这里一处，然后跑 `go test ./internal/version/`，
// 门禁会告诉你还有哪些锚点需要同步。
const Version = "0.3.0"

// History 已经发布过的历史版本号。
//
// 登记在册的门禁扫描时按语境放行：
//   - CHANGELOG 里的版本标题与条目；
//   - 版本对比 / 借鉴类历史文档（如 docs/BT-COMPARE-v0.2.0.md）。
//
// **不登记的版本号一律视为漂移**——这是刻意的：
// 宁可门禁误报一次让人确认，也不要"版本号忘了改"悄悄发出去。
//
// 关于版本线重置：项目在 v0.2.x 线上运行并对外提供过服务，
// 之后正式化迭代为 v0.1.0 稳定版。这是一次**有意的版本线重置**，
// 不是"从来没发过 0.2.x"。历史事实由 CHANGELOG 完整保留，
// 这里登记它们，是为了让门禁能区分"历史标记"与"当前版本"。
var History = []string{
	"0.2.0",
	"0.1.0",
	"0.1.1",
	"0.1.2",
	"0.2.1",
	"0.2.2",
	"0.1.3",
	"0.1.4",
	"0.1.5",
}

// All 返回当前版本与全部历史版本号。
func All() []string {
	out := make([]string, 0, len(History)+1)
	out = append(out, Version)
	out = append(out, History...)
	return out
}

// IsKnown 报告 v 是当前版本或已登记的历史版本。
func IsKnown(v string) bool {
	for _, k := range All() {
		if k == v {
			return true
		}
	}
	return false
}
