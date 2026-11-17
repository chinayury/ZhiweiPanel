// Package apps 定义 ZhiweiPanel「软件商店」的静态目录。
//
// 目录只描述「面板知道哪些软件」与它们的安装提示；真实的「已安装 / 可安装」
// 判定在 service 层探测（复用 driver 的 NginxStatus/PHPVersions 与 mysql 子系统，
// 其余走 exec.LookPath + 固定命令白名单）。这里不放任何探测逻辑，
// 避免与运行时驱动耦合。
package apps

// Entry 软件商店目录中的一条软件定义。
type Entry struct {
	ID          string
	Name        string
	Category    string
	Description string
	InstallHint string
}

// Catalog 面板已知的软件清单。
//
// 与宝塔软件商店对齐的常见分类：
//   - web：Web 服务器
//   - runtime：运行时（PHP / Node.js）
//   - database：数据库
//   - cache：缓存
//   - dev：开发/容器工具
var Catalog = []Entry{
	{
		ID:          "nginx",
		Name:        "Nginx",
		Category:    "web",
		Description: "高性能 HTTP 服务器 / 反向代理（面板核心依赖）",
		InstallHint: "apt install -y nginx",
	},
	{
		ID:          "php",
		Name:        "PHP-FPM",
		Category:    "runtime",
		Description: "PHP 运行时，支持多版本共存",
		InstallHint: "apt install -y php8.2-fpm",
	},
	{
		ID:          "mysql",
		Name:        "MySQL / MariaDB",
		Category:    "database",
		Description: "关系型数据库（站点「建库」功能依赖）",
		InstallHint: "重新运行 deploy/install.sh 并设 INSTALL_MYSQL=1",
	},
	{
		ID:          "redis",
		Name:        "Redis",
		Category:    "cache",
		Description: "内存键值缓存，常用于会话 / 队列 / 热点数据",
		InstallHint: "apt install -y redis-server",
	},
	{
		ID:          "docker",
		Name:        "Docker",
		Category:    "dev",
		Description: "容器运行时（面板 docker 部署模式需要）",
		InstallHint: "apt install -y docker.io",
	},
	{
		ID:          "nodejs",
		Name:        "Node.js",
		Category:    "runtime",
		Description: "JavaScript 运行时",
		InstallHint: "apt install -y nodejs npm",
	},
	{
		ID:          "memcached",
		Name:        "Memcached",
		Category:    "cache",
		Description: "分布式内存对象缓存",
		InstallHint: "apt install -y memcached",
	},
}
