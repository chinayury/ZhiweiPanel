// Package alert 实现告警引擎与通知渠道。
package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/security"
	"github.com/zhiwei/panel/internal/store"
)

// Engine 告警引擎。
type Engine struct {
	db     *store.DB
	client *http.Client

	// cooldown 抑制窗口：同一站点同一类型在该窗口内不重复告警。
	cooldown time.Duration
	mu       sync.Mutex
	lastSent map[string]time.Time
}

// NewEngine 创建告警引擎。
func NewEngine(db *store.DB) *Engine {
	return &Engine{
		db: db,
		client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: security.NoRedirectToInternal,
		},
		cooldown: 30 * time.Minute,
		lastSent: map[string]time.Time{},
	}
}

// shouldSend 判断是否处于抑制窗口内。
func (e *Engine) shouldSend(siteID, typ string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := siteID + "|" + typ
	if last, ok := e.lastSent[key]; ok && time.Since(last) < e.cooldown {
		return false
	}
	e.lastSent[key] = time.Now()
	return true
}

// Reset 清除抑制状态（用于测试）。
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSent = map[string]time.Time{}
}

// Raise 产生一条告警并推送到已配置渠道。
func (e *Engine) Raise(site *model.Site, rule model.AlertRule, message string) error {
	if !site.Alert.Enabled || !rule.Enabled {
		return nil
	}
	if !e.shouldSend(site.ID, rule.Type) {
		return nil
	}

	a := &model.Alert{
		ID:       store.NewID(),
		SiteID:   site.ID,
		SiteName: site.Name,
		Type:     rule.Type,
		Level:    rule.Level,
		Message:  message,
	}
	if err := e.db.SaveAlert(a); err != nil {
		return err
	}

	for _, ch := range site.Alert.Channels {
		if !ch.Enabled || strings.TrimSpace(ch.Target) == "" {
			continue
		}
		if err := e.notify(context.Background(), ch, a); err != nil {
			// 通知失败不阻断告警落库
			continue
		}
	}
	return nil
}

// NotifyGlobal 发送全局告警（无 site 上下文）。
//
// 当前 v0.2.0 只落库不通知：cron 失败、备份失败等全局事件写进 alerts 表，
// 管理员在"告警"页能看到。后续做"全局通知渠道"配置时再补通道通知。
func (e *Engine) NotifyGlobal(typ, level, message string) error {
	a := &model.Alert{
		ID:       store.NewID(),
		SiteID:   "",
		SiteName: "(全局)",
		Type:     typ,
		Level:    level,
		Message:  message,
	}
	return e.db.SaveAlert(a)
}

// TestChannel 测试通知渠道。
func (e *Engine) TestChannel(ctx context.Context, ch model.AlertChannel) error {
	a := &model.Alert{
		SiteName:  "测试",
		Type:      "test",
		Level:     "info",
		Message:   "这是一条来自智维面板的测试通知，收到即表示渠道配置正确。",
		CreatedAt: time.Now(),
	}
	return e.notify(ctx, ch, a)
}

// notify 按渠道类型发送通知。
func (e *Engine) notify(ctx context.Context, ch model.AlertChannel, a *model.Alert) error {
	switch ch.Type {
	case "email":
		return e.notifyEmail(ctx, ch.Target, a)
	case "webhook":
		if err := security.CheckExternalURL(ch.Target); err != nil {
			return fmt.Errorf("webhook 目标不安全: %w", err)
		}
		return e.notifyWebhook(ctx, ch.Target, a)
	case "dingtalk":
		if err := security.CheckExternalURL(ch.Target); err != nil {
			return fmt.Errorf("钉钉目标不安全: %w", err)
		}
		return e.notifyDingtalk(ctx, ch.Target, a)
	case "wecom":
		if err := security.CheckExternalURL(ch.Target); err != nil {
			return fmt.Errorf("企业微信目标不安全: %w", err)
		}
		return e.notifyWecom(ctx, ch.Target, a)
	default:
		return fmt.Errorf("不支持的通知渠道: %s", ch.Type)
	}
}

func (e *Engine) notifyWebhook(ctx context.Context, target string, a *model.Alert) error {
	payload, err := json.Marshal(map[string]any{
		"type":       "zhiweipanel_alert",
		"site_id":    a.SiteID,
		"site_name":  a.SiteName,
		"alert_type": a.Type,
		"level":      a.Level,
		"message":    a.Message,
		"time":       a.CreatedAt.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回 %d", resp.StatusCode)
	}
	return nil
}

func (e *Engine) notifyDingtalk(ctx context.Context, target string, a *model.Alert) error {
	// 支持带签名的 webhook：目标形如 https://oapi.dingtalk.com/robot/send?access_token=xxx
	fullURL, err := signDingtalk(target)
	if err != nil {
		return err
	}
	level := map[string]string{"critical": "警告", "warning": "警告", "info": "信息"}[a.Level]
	if level == "" {
		level = "信息"
	}
	payload, err := json.Marshal(map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": "智维面板站点告警",
			"text": fmt.Sprintf("### %s\n\n- **站点**：%s\n- **级别**：%s\n- **时间**：%s\n\n%s",
				"⚠️ 智维面板站点告警", a.SiteName, level, a.CreatedAt.Format("2006-01-02 15:04:05"), a.Message),
		},
	})
	if err != nil {
		return err
	}
	return e.postJSON(ctx, fullURL, payload)
}

func signDingtalk(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	secret := u.Query().Get("secret")
	if secret == "" {
		return raw, nil
	}
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (e *Engine) notifyWecom(ctx context.Context, target string, a *model.Alert) error {
	payload, err := json.Marshal(map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": fmt.Sprintf("## 智维面板站点告警\n> 站点：<font color=\"info\">%s</font>\n> 级别：%s\n> 时间：%s\n\n%s",
				a.SiteName, a.Level, a.CreatedAt.Format("2006-01-02 15:04:05"), a.Message),
		},
	})
	if err != nil {
		return err
	}
	return e.postJSON(ctx, target, payload)
}

// notifyEmail 通过本机 sendmail 发送（面板所在机器已配置 MTA 的场景）。
func (e *Engine) notifyEmail(ctx context.Context, target string, a *model.Alert) error {
	subject := fmt.Sprintf("[智维面板][%s] %s", strings.ToUpper(a.Level), a.SiteName)
	body := fmt.Sprintf("站点：%s\n级别：%s\n时间：%s\n\n%s\n",
		a.SiteName, a.Level, a.CreatedAt.Format("2006-01-02 15:04:05"), a.Message)
	msg := fmt.Sprintf("To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		target, subject, body)

	cmd := sendmailCmd(ctx, target, strings.NewReader(msg))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("发送邮件失败（请确认本机已配置 MTA）: %w", err)
	}
	return nil
}

func (e *Engine) postJSON(ctx context.Context, target string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("接口返回 %d", resp.StatusCode)
	}
	return nil
}
