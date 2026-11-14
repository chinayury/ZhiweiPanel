// Package logmgr 负责日志解析、流量统计聚合与防篡改基线。
package logmgr

import (
	"bufio"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// maxLogLineBytes 单行日志上限。超长行（超长 UA/Referer，攻击即可构造）
// 不应让整次读取失败，因此读取方对 ErrTooLong 做跳过处理。
const maxLogLineBytes = 1024 * 1024

// tailPollInterval EOF 后的轮询间隔。
const tailPollInterval = 300 * time.Millisecond

// 面板写入的访问日志格式：
// $remote_addr - $host [$time_local] "$request" $status $body_bytes_sent $request_time "$http_referer" "$http_user_agent" $upstream_response_time
//
// 分组顺序必须与下面的取值顺序严格一致，多一个少一个都会让整行错位：
//
//	1 remote_addr
//	2 host
//	3 time_local
//	4 request_method
//	5 request_uri
//	6 request_protocol
//	7 status
//	8 body_bytes_sent
//	9 request_time
//
// 10 http_referer
// 11 http_user_agent
// 12 upstream_response_time
var accessRe = regexp.MustCompile(
	`^(\S+) \S+ (\S+) \[([^\]]+)\] "(\S+) (\S+) (\S+)" (\d{3}) (\S+) (\S+) "([^"]*)" "([^"]*)" (\S+)$`)

// timeLayout 与 nginx $time_local 对应。
const timeLayout = "02/Jan/2006:15:04:05 -0700"

// ParseAccessLine 解析一行访问日志。
func ParseAccessLine(line string) (*model.LogRecord, error) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "/") && !strings.Contains(line, "\"") {
		return nil, model.ErrBadRequest
	}
	m := accessRe.FindStringSubmatch(line)
	if m == nil {
		return nil, model.ErrBadRequest
	}

	ts, err := time.Parse(timeLayout, m[3])
	if err != nil {
		return nil, err
	}

	rec := &model.LogRecord{
		RemoteAddr:   m[1],
		Host:         m[2],
		Time:         ts,
		Method:       m[4],
		Path:         m[5],
		Protocol:     m[6],
		Referer:      m[10],
		UA:           m[11],
		UpstreamTime: m[12],
	}

	status, _ := strconv.Atoi(m[7])
	rec.Status = status
	if m[8] != "-" {
		rec.Size, _ = strconv.ParseInt(m[8], 10, 64)
	}
	// request_time 是第 9 组
	if m[9] != "-" {
		rec.ReqTime, _ = strconv.ParseFloat(m[9], 64)
	}
	return rec, nil
}

// AccessLogFormat 返回面板写入 nginx 的 log_format 定义。
func AccessLogFormat() string {
	return `$remote_addr - $host [$time_local] "$request" $status $body_bytes_sent $request_time "$http_referer" "$http_user_agent" $upstream_response_time`
}

// ReadAccessLog 读取访问日志末尾 limit 行（倒序返回）。
//
// 必须真正读到文件尾部：早期用 Scanner 从头顺序读再提前 break，
// 对百万行级日志返回的是最旧的若干行——日志查看器显示远古记录、
// 5xx 告警窗口过滤后永远为 0（繁忙站点反而不告警）、流量统计张冠李戴。
// 这里先 seek 到估算的尾部窗口，再向前补齐。
func ReadAccessLog(path string, limit int) ([]*model.LogRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// 每条日志按 512 字节估窗口，至少 64KB；文件小时直接全读
	const estLine = 512
	window := int64(limit*estLine + 64*1024)
	if window >= info.Size() {
		window = 0
	}
	if window > 0 {
		if _, err := f.Seek(info.Size()-window, io.SeekStart); err != nil {
			return nil, err
		}
	}

	var out []*model.LogRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLogLineBytes)
	for sc.Scan() {
		rec, perr := ParseAccessLine(sc.Text())
		if perr != nil {
			continue
		}
		out = append(out, rec)
	}
	// 超长行不该让整次读取失败：跳过并继续，否则一条畸形行就会让
	// 5xx 告警与流量统计同时静默停摆。
	if err := sc.Err(); err != nil {
		if !errors.Is(err, bufio.ErrTooLong) {
			return nil, err
		}
	}
	// 反转为倒序（最新在前）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Tail 持续跟踪日志新增行，通过回调推送（follow 模式）。
// 返回 stop 函数用于终止；stop 可重复调用。
//
// 两个关键点：
//   - 遇到 EOF 不能退出，要轮询等待新行，否则"实时日志"读完成存量即死；
//   - 启动时 seek 到文件末尾，不把历史日志一次性推给前端。
func Tail(path string, onLine func(*model.LogRecord)) (stop func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// 从末尾开始，只推新行
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	done := make(chan struct{})
	var once sync.Once

	go func() {
		defer f.Close()
		reader := bufio.NewReader(f)
		for {
			select {
			case <-done:
				return
			default:
			}
			line, err := reader.ReadString('\n')
			if line != "" {
				if rec, perr := ParseAccessLine(line); perr == nil {
					select {
					case <-done:
						return
					default:
						onLine(rec)
					}
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					// EOF 只是暂时没有新行，等待后重试同一 fd
					select {
					case <-done:
						return
					case <-time.After(tailPollInterval):
					}
					continue
				}
				return
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }, nil
}

// Aggregate 把日志记录聚合为按日统计。
func Aggregate(records []*model.LogRecord, siteID string) *model.TrafficStat {
	stat := &model.TrafficStat{SiteID: siteID}
	ips := map[string]struct{}{}
	for _, r := range records {
		stat.Date = r.Time.Format("2006-01-02")
		stat.Requests++
		stat.Bytes += r.Size
		switch {
		case r.Status >= 500:
			stat.Status5xx++
		case r.Status >= 400:
			stat.Status4xx++
		case r.Status >= 300:
			stat.Status3xx++
		default:
			stat.Status2xx++
		}
		ips[r.RemoteAddr] = struct{}{}
	}
	stat.UniqueIPs = int64(len(ips))
	return stat
}

// ReadAndAggregate 读取日志并聚合为统计数据。
func ReadAndAggregate(path, siteID string) (*model.TrafficStat, error) {
	records, err := ReadAccessLog(path, 50000)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return Aggregate(records, siteID), nil
}
