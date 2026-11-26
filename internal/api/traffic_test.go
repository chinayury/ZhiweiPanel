package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// 流量统计 API：reset 必须显式确认；站点 stats 返回精确状态码字段。
func TestTrafficResetAndStatsAPI(t *testing.T) {
	srv, db := new2FAServer(t)

	if err := db.ApplyTrafficBatch(&model.TrafficBatch{
		Checkpoint: model.TrafficLogOffset{SiteID: "s1", Path: "/x", Offset: 10},
		Days: []*model.TrafficDay{{
			Stat:        &model.TrafficStat{Date: time.Now().Format("2006-01-02"), SiteID: "s1", Requests: 2, Bytes: 200, Status2xx: 1, Status5xx: 1},
			IPs:         []string{"1.1.1.1"},
			StatusCodes: map[int]int64{200: 1, 500: 1},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// 错误确认词 / GET：拒绝。
	rec := withSession(t, srv, db, http.MethodPost, "/api/system/traffic/reset", `{"confirm":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("错误确认词应 400，得到 %d", rec.Code)
	}
	rec = withSession(t, srv, db, http.MethodGet, "/api/system/traffic/reset", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405，得到 %d", rec.Code)
	}

	// 站点统计：返回 status_codes。
	rec = withSession(t, srv, db, http.MethodGet, "/api/sites/s1/stats?days=7", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats 应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Stats       []model.TrafficStat        `json:"stats"`
		StatusCodes []model.TrafficStatusCount `json:"status_codes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Stats) != 1 || len(body.StatusCodes) != 2 {
		t.Fatalf("stats/status_codes 数量不对: %+v", body)
	}

	// 正确确认词：清空。
	rec = withSession(t, srv, db, http.MethodPost, "/api/system/traffic/reset", `{"confirm":"RESET"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset 应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if stats, _ := db.GetTrafficStats("s1", 7); len(stats) != 0 {
		t.Fatalf("reset 后应无统计，得到 %d", len(stats))
	}
}
