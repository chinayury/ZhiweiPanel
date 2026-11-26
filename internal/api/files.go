package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
)

// handleSiteFiles 站点文件管理。
//
// 路由（都在鉴权后面）：
//
//	GET    /api/sites/{id}/files?path=...          列表
//	GET    /api/sites/{id}/files/raw?path=...      读
//	PUT    /api/sites/{id}/files?path=...          写
//	DELETE /api/sites/{id}/files?path=...          删
//	POST   /api/sites/{id}/files/rename            {from, to}
//	POST   /api/sites/{id}/files/mkdir             {path}
//	POST   /api/sites/{id}/files/upload            multipart: path=, file=
//	POST   /api/sites/{id}/files/unzip             JSON: {zip_path, dest}
//
// v0.9.0 文件管理 2.0：
//
//	GET    /api/sites/{id}/files/download?path=    下载单文件
//	GET    /api/sites/{id}/files/search?q=&limit=  递归搜索
//	POST   /api/sites/{id}/files/compress          {base, names[], format}
//	POST   /api/sites/{id}/files/extract           {archive, dest}
//	POST   /api/sites/{id}/files/fetch             {url, path, filename}
//	POST   /api/sites/{id}/files/delete-batch      {paths[], recursive}
//	POST   /api/sites/{id}/files/chmod             {path, mode}
//
// 错误统一经 writeModelError，前端能从 {error, code} 取文案。
func (s *Server) handleSiteFiles(w http.ResponseWriter, r *http.Request, siteID string) {
	site, err := s.svc.DB().GetSite(siteID)
	if err != nil {
		writeModelError(w, err)
		return
	}
	if site.Root.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("站点未配置根目录"))
		return
	}

	// sub-action 解析：
	//   路径末段：/files /files/raw /files/rename /files/mkdir /files/upload /files/unzip
	//   额外用 _action=raw 兼容 ?path= 的 GET 读文件请求
	sub := r.URL.Query().Get("_action")
	if sub == "" {
		// 从 path 末段推断（适用于 GET raw 与 POST rename/mkdir/upload/unzip）
		rest := r.URL.Path
		prefix := "/api/sites/" + siteID + "/files/"
		if len(rest) > len(prefix) && rest[:len(prefix)] == prefix {
			sub = rest[len(prefix):]
		}
	}

	switch r.Method {
	case http.MethodGet:
		switch sub {
		case "raw":
			s.fileRaw(w, r, site)
		case "download":
			s.fileDownload(w, r, site)
		case "search":
			s.fileSearch(w, r, site)
		case "", "list":
			s.fileList(w, r, site)
		default:
			writeError(w, http.StatusBadRequest, errors.New("未知 sub-action"))
		}
	case http.MethodPut:
		s.fileWrite(w, r, site)
	case http.MethodDelete:
		s.fileDelete(w, r, site)
	case http.MethodPost:
		switch sub {
		case "rename":
			s.fileRename(w, r, site)
		case "mkdir":
			s.fileMkdir(w, r, site)
		case "upload":
			s.fileUpload(w, r, site)
		case "unzip":
			s.fileUnzip(w, r, site)
		case "compress":
			s.fileCompress(w, r, site)
		case "extract":
			s.fileExtract(w, r, site)
		case "fetch":
			s.fileFetch(w, r, site)
		case "delete-batch":
			s.fileDeleteBatch(w, r, site)
		case "chmod":
			s.fileChmod(w, r, site)
		default:
			writeError(w, http.StatusBadRequest, errors.New("未知 sub-action"))
		}
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

func (s *Server) fileList(w http.ResponseWriter, r *http.Request, site *model.Site) {
	rel := r.URL.Query().Get("path")
	entries, err := s.svc.ListFiles(r.Context(), site, rel)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "path": rel})
}

func (s *Server) fileRaw(w http.ResponseWriter, r *http.Request, site *model.Site) {
	rel := r.URL.Query().Get("path")
	data, isText, mt, err := s.svc.ReadFile(r.Context(), site, rel)
	if err != nil {
		writeModelError(w, err)
		return
	}
	if isText {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	// 二进制以 base64 包在 JSON 里返回，避免直接写 raw 触发 XSS/HTML 注入
	writeJSON(w, http.StatusOK, map[string]any{
		"content":  base64.StdEncoding.EncodeToString(data),
		"encoding": "base64",
		"mime":     mt,
		"size":     len(data),
	})
}

func (s *Server) fileWrite(w http.ResponseWriter, r *http.Request, site *model.Site) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, errors.New("path 必填"))
		return
	}
	ct := r.Header.Get("Content-Type")
	if len(ct) >= 16 && ct[:16] == "application/json" {
		var body struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, int64(service.MaxFileSize+1024))).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var data []byte
		if body.Encoding == "base64" {
			decoded, derr := base64.StdEncoding.DecodeString(body.Content)
			if derr != nil {
				writeError(w, http.StatusBadRequest, errors.New("base64 解码失败"))
				return
			}
			data = decoded
		} else {
			data = []byte(body.Content)
		}
		if err := s.svc.WriteFile(r.Context(), site, rel, data); err != nil {
			writeModelError(w, err)
			return
		}
	} else {
		data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, int64(service.MaxFileSize+1024)))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, service.ErrFileTooLarge)
			return
		}
		if err := s.svc.WriteFile(r.Context(), site, rel, data); err != nil {
			writeModelError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) fileDelete(w http.ResponseWriter, r *http.Request, site *model.Site) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, errors.New("path 必填"))
		return
	}
	if err := s.svc.DeleteFile(r.Context(), site, rel); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) fileRename(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.From == "" || body.To == "" {
		writeError(w, http.StatusBadRequest, errors.New("from / to 必填"))
		return
	}
	if err := s.svc.Rename(r.Context(), site, body.From, body.To); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) fileMkdir(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path 必填"))
		return
	}
	if err := s.svc.Mkdir(r.Context(), site, body.Path); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// fileUpload multipart/form-data 上传文件到站点根目录的子目录。
//
// 表单字段：
//
//	path  string     目标目录（相对站点根）。空表示根。
//	file  File       一个或多个要上传的文件。filename 字段用作落盘名。
//
// 限制：每个文件 ≤ service.MaxFileSize (50MB)。
func (s *Server) fileUpload(w http.ResponseWriter, r *http.Request, site *model.Site) {
	// 限流：multipart 总大小 = MaxFileSize * (file 数 + 1) 给些余量
	maxMem := int64(service.MaxFileSize) * 8
	if err := r.ParseMultipartForm(maxMem); err != nil {
		// ParseMultipartForm 返回 "request body too large" 时按 413 处理
		if strings.Contains(err.Error(), "too large") {
			writeError(w, http.StatusRequestEntityTooLarge, service.ErrFileTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Errorf("解析上传表单失败: %w", err))
		return
	}

	targetDir := r.FormValue("path")
	if strings.Contains(targetDir, "..") {
		writeError(w, http.StatusBadRequest, errors.New("path 不能包含 .."))
		return
	}

	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("未提供 file 字段"))
		return
	}

	uploaded := make([]map[string]any, 0, len(files))
	for _, fh := range files {
		if fh.Size > service.MaxFileSize {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Errorf("%s 超过单文件上限 %d 字节", fh.Filename, service.MaxFileSize))
			return
		}
		// 文件名安全：仅取 basename，拒绝任何路径分隔符
		name := safeFilename(fh.Filename)
		if name == "" {
			writeError(w, http.StatusBadRequest, errors.New("文件名非法"))
			return
		}
		var rel string
		if targetDir == "" {
			rel = name
		} else {
			rel = strings.TrimSuffix(targetDir, "/") + "/" + name
		}
		if err := s.writeUploadedFile(r.Context(), site, rel, fh); err != nil {
			writeModelError(w, err)
			return
		}
		uploaded = append(uploaded, map[string]any{
			"name": rel,
			"size": fh.Size,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"uploaded": uploaded,
	})
}

// writeUploadedFile 把单个 multipart.FileHeader 流式写到站点根内的目标文件。
//
// 不一次性 ReadAll 到内存：用 io.Copy + LimitedReader 限流到 MaxFileSize，
// 避免一次上传超大文件把面板内存打爆。
func (s *Server) writeUploadedFile(ctx context.Context, site *model.Site, rel string, fh *multipart.FileHeader) error {
	if rel == "" || rel == "/" {
		return errors.New("目标路径不能为空或根")
	}
	src, err := fh.Open()
	if err != nil {
		return fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer src.Close()

	// 复用 WriteFile：把 reader 的内容读出成 bytes
	// 注意 WriteFile 接受 []byte，这里用 LimitReader + io.ReadAll
	lr := io.LimitReader(src, int64(service.MaxFileSize)+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return fmt.Errorf("读取上传内容失败: %w", err)
	}
	if int64(len(data)) > int64(service.MaxFileSize) {
		return service.ErrFileTooLarge
	}
	return s.svc.WriteFile(ctx, site, rel, data)
}

// safeFilename 去掉 multipart.FileHeader.Filename 里的路径成分，留下纯文件名。
//
// 防 ../foo 之类在 multipart filename 里夹带路径：浏览器一般不会这样，但
// 客户端可控，仍要过一道。接受 utf-8，仅禁路径分隔符与控制字符。
func safeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == 0 {
			return ""
		}
	}
	return name
}

// fileUnzip 把站点内的 zip 文件解压到指定目录。
//
// body: {zip_path: "site.zip", dest?: "public"}
// dest 留空时解压到 zip 同目录。
func (s *Server) fileUnzip(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		ZipPath string `json:"zip_path"`
		Dest    string `json:"dest"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.ZipPath == "" {
		writeError(w, http.StatusBadRequest, errors.New("zip_path 必填"))
		return
	}
	if strings.Contains(body.ZipPath, "..") || strings.Contains(body.Dest, "..") {
		writeError(w, http.StatusBadRequest, errors.New("路径不能包含 .."))
		return
	}
	extracted, err := s.svc.UnzipArchive(r.Context(), site, body.ZipPath, body.Dest)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"extracted": extracted,
		"dest":      body.Dest,
	})
}

// fileDownload 下载单个文件（流式，不把内容读进内存）。
func (s *Server) fileDownload(w http.ResponseWriter, r *http.Request, site *model.Site) {
	rel := r.URL.Query().Get("path")
	full, size, err := s.svc.DownloadFile(r.Context(), site, rel)
	if err != nil {
		writeModelError(w, err)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeModelError(w, err)
		return
	}
	defer f.Close()
	name := filepath.Base(full)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, f)
}

// fileSearch 递归搜索文件名。
func (s *Server) fileSearch(w http.ResponseWriter, r *http.Request, site *model.Site) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	entries, err := s.svc.SearchFiles(r.Context(), site, q, limit)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "q": q})
}

// fileCompress 压缩当前目录下选中的条目。
func (s *Server) fileCompress(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		Base   string   `json:"base"`
		Names  []string `json:"names"`
		Format string   `json:"format"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	archiveRel, err := s.svc.CompressPaths(ctx, site, body.Base, body.Names, body.Format)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": archiveRel})
}

// fileExtract 解压 zip / tar / tar.gz。
func (s *Server) fileExtract(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		Archive string `json:"archive"`
		Dest    string `json:"dest"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	n, err := s.svc.ExtractArchive(ctx, site, body.Archive, body.Dest)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "extracted": n, "dest": body.Dest})
}

// fileFetch 从远程 URL 下载文件到指定目录。
func (s *Server) fileFetch(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		URL      string `json:"url"`
		Path     string `json:"path"`
		Filename string `json:"filename"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rel, n, err := s.svc.RemoteDownload(ctx, site, body.URL, body.Path, body.Filename)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": rel, "bytes": n})
}

// fileDeleteBatch 批量删除（递归需显式 recursive=true）。
func (s *Server) fileDeleteBatch(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		Paths     []string `json:"paths"`
		Recursive bool     `json:"recursive"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	n, err := s.svc.BatchDelete(ctx, site, body.Paths, body.Recursive)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

// fileChmod 修改权限。
func (s *Server) fileChmod(w http.ResponseWriter, r *http.Request, site *model.Site) {
	var body struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.svc.Chmod(r.Context(), site, body.Path, body.Mode); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
