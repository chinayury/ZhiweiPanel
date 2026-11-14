package service

import (
	"context"
	"log"
	"strings"

	"github.com/zhiwei/panel/internal/model"
)

// RebindRenewedCert 把站点刷新到刚续签出的证书。
//
// 续签现在**复用原证书 ID**（见 acme.saveCertificate 的说明），
// 所以证书文件路径不变、内容被覆盖，nginx 配置一个字都不用改；
// 但站点的 CertRef 里记着有效期、颁发者等元数据，必须同步刷新——
// 否则"证书临期告警"用的还是旧日期，会一直误报快到期。
//
// 为什么必须有这一步：早期版本里，续签产出的是全新 ID、全新文件路径，
// 而**手动续签**的 API 路径既不重绑站点、也不更新 SSL 路径，
// 于是接口返回 200、新证书进了库，nginx 却还在 serve 旧证书。
// 等旧证书自然过期，站点 HTTPS 直接挂掉，而用户完全看不出原因。
//
// 站点按域名匹配：证书的任一域名与站点的任一域名相同即视为绑定。
func (s *Service) RebindRenewedCert(ctx context.Context, renewed *model.Cert) error {
	if renewed == nil {
		return nil
	}
	sites, err := s.db.ListSites()
	if err != nil {
		return err
	}
	changed := 0
	for _, site := range sites {
		if !siteUsesCert(site, renewed) {
			continue
		}
		site.Cert = &model.CertRef{
			CertID:    renewed.ID,
			Domains:   renewed.Domains,
			NotBefore: renewed.NotBefore,
			NotAfter:  renewed.NotAfter,
			Issuer:    renewed.Issuer,
			Source:    renewed.Source,
		}
		// 证书路径也要跟着走（续签复用 ID 时路径不变，但显式赋值更稳，
		// 也能兼容未来"续签换目录"的实现）。
		site.SSL.CertFile = renewed.CertFile
		site.SSL.KeyFile = renewed.KeyFile
		if _, err := s.UpdateSite(ctx, site.ID, site); err != nil {
			log.Printf("[cert] 续签后重绑站点 %s 失败: %v", site.Name, err)
			continue
		}
		log.Printf("[cert] 站点 %s 已刷新到证书 %s（有效期至 %s）",
			site.Name, renewed.ID, renewed.NotAfter.Format("2006-01-02"))
		changed++
	}
	if changed == 0 {
		return nil
	}
	// 重新下发配置，让 nginx 读到新的证书文件内容。
	if err := s.ApplyAllConfig(ctx); err != nil {
		return err
	}
	return nil
}

// siteUsesCert 判断站点是否使用了该证书。
//
// 两条判据都算：
//   - 站点显式绑定了这个证书 ID（site.Cert.CertID）；
//   - 证书域名与站点域名有交集（按域名反查，用于"签发时就绑上"的情况）。
func siteUsesCert(site *model.Site, cert *model.Cert) bool {
	if site == nil || cert == nil {
		return false
	}
	if site.Cert != nil && site.Cert.CertID == cert.ID {
		return true
	}
	for _, d := range site.Domains {
		for _, cd := range cert.Domains {
			if strings.EqualFold(d, cd) {
				return true
			}
		}
	}
	return false
}
