package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/backup"
	"github.com/zhiwei/panel/internal/model"
)

// ---------- 备份 ----------

// handleBackups 列表 + 手动创建。
//
// GET  /api/backups?kind=&target=&limit=
// POST /api/backups  body={kind, target, note}
func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		kind := r.URL.Query().Get("kind")
		target := r.URL.Query().Get("target")
		limit := strconvParseInt(r.URL.Query().Get("limit"), 100)
		list, err := s.svc.DB().ListBackups(kind, target, limit)
		if err != nil {
			writeModelError(w, err)
			return
		}
		if list == nil {
			list = []*model.Backup{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"backups": list})
	case http.MethodPost:
		var body struct {
			Kind   string `json:"kind"`
			Target string `json:"target"`
			Note   string `json:"note"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var (
			b   *model.Backup
			err error
		)
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		switch body.Kind {
		case "site":
			site, gerr := s.svc.DB().GetSite(body.Target)
			if gerr != nil {
				writeModelError(w, gerr)
				return
			}
			b, err = backup.BackupSite(ctx, s.svc, site)
		case "database":
			if s.svc.MySQL() == nil {
				writeError(w, http.StatusBadRequest, errors.New("MySQL 未启用"))
				return
			}
			b, err = backup.BackupDatabase(ctx, s.svc, body.Target)
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("不支持的备份类型: %s", body.Kind))
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		b.Note = body.Note
		if err := s.svc.DB().SaveBackup(b); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, b)
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// handleBackupByID 详情 / 下载 / 恢复 / 删除。
//
// GET    /api/backups/{id}
// GET    /api/backups/{id}/download       → tar.gz 二进制
// POST   /api/backups/{id}/restore        body={target_root: "..."}  (站点)
// DELETE /api/backups/{id}
func (s *Server) handleBackupByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/backups/")
	if rest == "" {
		writeError(w, http.StatusBadRequest, errors.New("id required"))
		return
	}
	// 切出 id 与 action
	var id, action string
	if i := strings.Index(rest, "/"); i < 0 {
		id = rest
	} else {
		id = rest[:i]
		action = rest[i+1:]
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, errors.New("id required"))
		return
	}
	b, err := s.svc.DB().GetBackup(id)
	if err != nil {
		writeModelError(w, err)
		return
	}
	switch action {
	case "":
		if r.Method == http.MethodDelete {
			_ = os.Remove(b.Path)
			if err := s.svc.DB().DeleteBackup(id); err != nil {
				writeModelError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, b)
	case "download":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
			return
		}
		if _, err := os.Stat(b.Path); err != nil {
			writeError(w, http.StatusNotFound, errors.New("备份文件已丢失"))
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+id+backupExt(b.Kind)+`"`)
		http.ServeFile(w, r, b.Path)
	case "restore":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
			return
		}
		if b.Kind != model.BackupSite {
			writeError(w, http.StatusBadRequest, errors.New("仅支持恢复站点备份"))
			return
		}
		// v0.2.0 简化：只对 site 备份预览 nginx.conf；数据文件恢复由用户手动 tar xzf。
		body, rerr := readMemberFromTarGz(b.Path, "nginx.conf")
		if rerr != nil {
			writeError(w, http.StatusInternalServerError, rerr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"preview":       string(body),
			"preview_bytes": len(body),
		})
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown action"))
	}
}

// backupExt 给下载文件名加扩展名。
func backupExt(k model.BackupKind) string {
	if k == model.BackupDatabase {
		return ".sql.gz"
	}
	return ".tar.gz"
}

// readMemberFromTarGz 从 tar.gz 里读指定 member 的内容（一次性读入内存，
// 备份预览 nginx.conf 这种小文件足够）。
func readMemberFromTarGz(path, name string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return nil, err
		}
		if hdr.Name == name {
			// 1MB 上限，nginx.conf 不会超
			return io.ReadAll(io.LimitReader(tr, 1<<20))
		}
	}
}

func strconvParseInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
