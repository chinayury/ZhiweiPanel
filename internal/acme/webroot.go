package acme

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-acme/lego/v4/challenge"
)

// WebrootProvider 通过把挑战文件写入站点根目录来完成 HTTP-01 验证。
//
// 为什么不用 lego 内置的 ProviderServer：内置实现要自己监听 80 端口，
// 而 80 端口通常已被 nginx 占用，会直接 bind 失败。
// 写入 webroot 后由 nginx 直接提供静态文件，无需重载、无需抢占端口，
// 这是主流面板（宝塔等）采用的标准做法。
type WebrootProvider struct {
	// Roots 域名 → 站点根目录映射。申请多域名时各域名可以落在不同站点。
	Roots map[string]string
}

// NewWebrootProvider 创建 webroot 挑战提供者。
func NewWebrootProvider(roots map[string]string) *WebrootProvider {
	if roots == nil {
		roots = map[string]string{}
	}
	return &WebrootProvider{Roots: roots}
}

// challengePath 返回挑战文件的落盘路径。
// 路径必须严格遵循 ACME 规范：/.well-known/acme-challenge/<token>
func (w *WebrootProvider) challengePath(domain, token string) (string, error) {
	root, ok := w.Roots[domain]
	if !ok || root == "" {
		return "", fmt.Errorf("域名 %s 未配置站点根目录，无法完成 HTTP-01 验证", domain)
	}
	dir := filepath.Join(root, ".well-known", "acme-challenge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建挑战目录失败: %w", err)
	}
	return filepath.Join(dir, token), nil
}

// Present 写入挑战文件。
func (w *WebrootProvider) Present(domain, token, keyAuth string) error {
	path, err := w.challengePath(domain, token)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(keyAuth), 0o644); err != nil {
		return fmt.Errorf("写入挑战文件失败: %w", err)
	}
	return nil
}

// CleanUp 删除挑战文件。
func (w *WebrootProvider) CleanUp(domain, token, keyAuth string) error {
	path, err := w.challengePath(domain, token)
	if err != nil {
		return err
	}
	// 文件可能已被清理，忽略不存在的情况
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// 确保实现 challenge.Provider 接口。
var _ challenge.Provider = (*WebrootProvider)(nil)
