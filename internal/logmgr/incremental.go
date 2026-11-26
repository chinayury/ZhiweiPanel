package logmgr

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/zhiwei/panel/internal/model"
)

// 增量采集参数。
const (
	// 单次最多处理的日志行数。繁忙站点一轮可能新增几十万行，
	// 一次全读会长时间占用 goroutine；留到下一轮继续（checkpoint 已推进）。
	maxIncrementalLines = 200000
	// 采样长度：用 offset 前的最后 64 字节识别"截断后又长回原大小"。
	sampleBytes = 64
)

// ReadIncremental 从 checkpoint 之后读取新增日志，按日聚合。
//
// 与 ReadAccessLog 的区别：后者是"读者视图"（读末尾 N 行，可重复），
// 本函数是"统计视图"（只读增量，推进 checkpoint，不重复计数）。
//
// 首次采集（prev == nil）从文件末尾开始：升级前已存在的历史日志可能已经
// 被旧逻辑重复计入，再补算一次只会让虚高更严重。新站点的 checkpoint 在
// CreateSite 时以 offset=0 预置，因此新站点首轮会从文件头开始。
func ReadIncremental(path, siteID string, prev *model.TrafficLogOffset) (*model.TrafficBatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	dev, ino := fileIdentity(info)
	start := int64(0)
	startedAtEnd := false

	switch {
	case prev == nil:
		start = info.Size()
		startedAtEnd = true
	case prev.Path != "" && prev.Path != path:
		// 站点根目录被改过：新路径的文件是另一份日志，从末尾起步避免重复。
		start = info.Size()
		startedAtEnd = true
	case info.Size() < prev.Offset:
		// 截断（copytruncate）：从头读新内容。
		start = 0
	case prev.Dev != 0 && prev.Ino != 0 && (prev.Dev != dev || prev.Ino != ino):
		// 轮转/替换：新 inode，从头读。
		start = 0
	case prev.Offset > 0 && prev.Sample != "" && !verifySample(f, prev.Offset, prev.Sample):
		// 文件被截断后又长回 >= 原大小：size 检查失效，用采样识别。
		start = 0
	default:
		start = prev.Offset
	}
	if start < 0 {
		start = 0
	}
	if start > info.Size() {
		start = info.Size()
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	offset := start
	records := make([]*model.LogRecord, 0, 256)
	lines := 0
	// 用"处理过的行数"而不是"解析成功的记录数"封顶：
	// 全是畸形行的日志同样不能在一轮里读到底。
	for lines < maxIncrementalLines {
		lines++
		line, consumed, rerr := readIncrementalLine(reader)
		if rerr == nil {
			offset += consumed
			if line != nil {
				if rec, perr := ParseAccessLine(string(line)); perr == nil {
					records = append(records, rec)
				}
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				// 最后一行没有换行符：不推进 offset，下一轮从行首重读。
				break
			}
			return nil, rerr
		}
	}

	batch := &model.TrafficBatch{
		Checkpoint: model.TrafficLogOffset{
			SiteID: siteID,
			Path:   path,
			Dev:    dev,
			Ino:    ino,
			Offset: offset,
			Sample: readSample(f, offset),
		},
		Days:         aggregateByDate(records, siteID),
		StartedAtEnd: startedAtEnd,
	}
	return batch, nil
}

// readIncrementalLine 分段读取，超出上限时只累计字节数。只有完整行才推进 checkpoint。
func readIncrementalLine(reader *bufio.Reader) ([]byte, int64, error) {
	var saved []byte
	var consumed int64
	for {
		part, err := reader.ReadSlice('\n')
		consumed += int64(len(part))
		if consumed <= maxLogLineBytes {
			if err == nil && saved == nil {
				return part, consumed, nil
			}
			saved = append(saved, part...)
		} else {
			saved = nil
		}
		if err != bufio.ErrBufferFull {
			return saved, consumed, err
		}
	}
}

// verifySample 校验 offset 之前的采样是否与 checkpoint 一致。
func verifySample(f *os.File, offset int64, want string) bool {
	got, err := sampleAt(f, offset)
	if err != nil {
		return false
	}
	return got == want
}

// readSample 读取 offset 之前最后 sampleBytes 字节的摘要。
func readSample(f *os.File, offset int64) string {
	got, err := sampleAt(f, offset)
	if err != nil {
		return ""
	}
	return got
}

func sampleAt(f *os.File, offset int64) (string, error) {
	if offset <= 0 {
		return "", nil
	}
	n := int64(sampleBytes)
	if offset < n {
		n = offset
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, offset-n); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// aggregateByDate 把记录按本地日期拆分聚合。
//
// 旧实现 Aggregate 把整批记录都记到最后一条记录的日期上——跨午夜的
// 那批（例如 00:03 采集）会把前一天 23:5x 的流量记到今天。
func aggregateByDate(records []*model.LogRecord, siteID string) []*model.TrafficDay {
	type dayAgg struct {
		stat  *model.TrafficStat
		ips   map[string]struct{}
		codes map[int]int64
	}
	m := make(map[string]*dayAgg)
	for _, r := range records {
		if r == nil {
			continue
		}
		date := r.Time.Format("2006-01-02")
		d := m[date]
		if d == nil {
			d = &dayAgg{
				stat:  &model.TrafficStat{Date: date, SiteID: siteID},
				ips:   map[string]struct{}{},
				codes: map[int]int64{},
			}
			m[date] = d
		}
		d.stat.Requests++
		d.stat.Bytes += r.Size
		switch {
		case r.Status >= 500:
			d.stat.Status5xx++
		case r.Status >= 400:
			d.stat.Status4xx++
		case r.Status >= 300:
			d.stat.Status3xx++
		default:
			d.stat.Status2xx++
		}
		if r.RemoteAddr != "" {
			d.ips[r.RemoteAddr] = struct{}{}
		}
		if r.Status > 0 {
			d.codes[r.Status]++
		}
	}

	dates := make([]string, 0, len(m))
	for date := range m {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	out := make([]*model.TrafficDay, 0, len(dates))
	for _, date := range dates {
		d := m[date]
		ips := make([]string, 0, len(d.ips))
		for ip := range d.ips {
			ips = append(ips, ip)
		}
		out = append(out, &model.TrafficDay{
			Stat:        d.stat,
			IPs:         ips,
			StatusCodes: d.codes,
		})
	}
	return out
}
