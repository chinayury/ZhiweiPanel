package api

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/backup"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/mysql"
)

// 数据库中心（v0.9.0）。
//
// 只管理面板自己创建的 site_* 库：列表、改密、导出、导入、优化。
// 不提供建库/删库/phpMyAdmin 式表编辑——库的生命周期仍绑定站点，
// 这样"删站联动删库"的既有语义不会被绕过。

const maxDatabaseImportBytes = 100 << 20 // 100MB（解压后）

var errImportTooLarge = errors.New("导入文件超过 100MB 上限")

// handleDatabases GET /api/databases
func (s *Server) handleDatabases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	list, err := s.svc.ListDatabases(r.Context())
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": list})
}

// handleDatabaseByID 处理 /api/databases/{name}/{action}。
func (s *Server) handleDatabaseByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/databases/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	// 先做名字白名单：mysql/sys/performance_schema 等系统库在
	// 任何动作之前就拒绝，避免它们进入后续 SQL/备份路径。
	if err := mysql.ValidateManagedDatabaseName(name); err != nil {
		writeError(w, http.StatusNotFound, model.ErrNotFound)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
			return
		}
		ov, err := s.svc.GetDatabase(r.Context(), name)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ov)
	case "password":
		if !requirePost(w, r) {
			return
		}
		pwd, err := s.svc.ResetDatabasePassword(r.Context(), name)
		if err != nil {
			writeModelError(w, err)
			return
		}
		// 明文只在这里出现一次；前端应弹窗让用户立即保存。
		writeJSON(w, http.StatusOK, map[string]any{
			"name":          name,
			"password":      pwd,
			"show_password": true,
		})
	case "optimize":
		if !requirePost(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		n, err := s.svc.OptimizeDatabase(ctx, name)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tables": n})
	case "export":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
			return
		}
		s.handleDatabaseExport(w, r, name)
	case "import":
		if !requirePost(w, r) {
			return
		}
		s.handleDatabaseImport(w, r, name)
	default:
		writeError(w, http.StatusNotFound, model.ErrNotFound)
	}
}

// handleDatabaseExport 生成 mysqldump 临时文件并下载，下载后删除。
func (s *Server) handleDatabaseExport(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := s.svc.GetDatabase(r.Context(), name); err != nil {
		writeModelError(w, err)
		return
	}
	b, err := backup.BackupDatabase(r.Context(), s.svc, name)
	if err != nil {
		writeModelError(w, err)
		return
	}
	defer func() { _ = os.Remove(b.Path) }()

	f, err := os.Open(b.Path)
	if err != nil {
		writeModelError(w, err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.sql.gz"`, name))
	w.Header().Set("Content-Length", strconv.FormatInt(b.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, f)
}

// handleDatabaseImport 接收 .sql / .sql.gz，落临时文件后交给 mysql 客户端。
//
// 先落盘而不是直接流给 mysql：这样可以准确限制解压后的体积，
// 也能在 SQL 文件不是 gzip 时报出清晰错误。
func (s *Server) handleDatabaseImport(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := s.svc.GetDatabase(r.Context(), name); err != nil {
		writeModelError(w, err)
		return
	}
	// 请求体上限 = 100MB + 表单开销；解压后另有 100MB 限制。
	r.Body = http.MaxBytesReader(w, r.Body, maxDatabaseImportBytes+(8<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("解析上传失败（单文件上限 100MB）: %w", err))
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("缺少 file 字段"))
		return
	}
	defer f.Close()

	lowerName := strings.ToLower(hdr.Filename)
	isGzip := strings.HasSuffix(lowerName, ".gz")
	if !strings.HasSuffix(lowerName, ".sql") && !isGzip {
		writeError(w, http.StatusBadRequest, errors.New("只支持 .sql 或 .sql.gz 文件"))
		return
	}

	tmpDir := filepath.Join(s.svc.DataDir(), "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		writeModelError(w, err)
		return
	}
	tmp, err := os.CreateTemp(tmpDir, "db-import-*.sql")
	if err != nil {
		writeModelError(w, err)
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	var src io.Reader = f
	if isGzip {
		zr, err := gzip.NewReader(f)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("gzip 文件损坏: %w", err))
			return
		}
		defer zr.Close()
		src = zr
	}
	written, err := io.Copy(tmp, io.LimitReader(src, maxDatabaseImportBytes+1))
	if err != nil {
		writeModelError(w, err)
		return
	}
	if written > maxDatabaseImportBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errImportTooLarge)
		return
	}
	if err := tmp.Sync(); err != nil {
		writeModelError(w, err)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeModelError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := s.svc.ImportDatabase(ctx, name, tmp); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": written})
}
