package app

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type fileEntry struct {
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	ServerPath      string    `json:"serverPath"`
	Type            string    `json:"type"`
	Size            int64     `json:"size"`
	ModifiedAt      time.Time `json:"modifiedAt"`
	MIME            string    `json:"mime,omitempty"`
	Preview         string    `json:"preview,omitempty"`
	PreviewTooLarge bool      `json:"previewTooLarge,omitempty"`
}

type fileListResponse struct {
	Path       string      `json:"path"`
	ServerPath string      `json:"serverPath"`
	Entries    []fileEntry `json:"entries"`
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	relative, absolute, err := s.paths.resolveExisting(rawPath)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	info, err := os.Stat(absolute)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("目标不是文件夹"))
		return
	}

	directoryEntries, err := os.ReadDir(absolute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法读取目录"))
		return
	}

	entries := make([]fileEntry, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		if relative == "" && directoryEntry.Name() == metadataDirectoryName {
			continue
		}
		if directoryEntry.Type()&os.ModeSymlink != 0 {
			continue
		}
		entryInfo, err := directoryEntry.Info()
		if err != nil {
			continue
		}
		entryRelative := filepath.ToSlash(filepath.Join(relative, directoryEntry.Name()))
		entryType := "file"
		entryMIME := ""
		entryPreview := ""
		entryPreviewTooLarge := false
		if entryInfo.IsDir() {
			entryType = "directory"
		} else if entryInfo.Mode().IsRegular() {
			entryMIME = mimeTypeForName(directoryEntry.Name())
			entryPreview, entryPreviewTooLarge = previewKindForSize(
				directoryEntry.Name(),
				entryMIME,
				entryInfo.Size(),
				s.config.MaxPreviewSize,
			)
		} else {
			continue
		}
		entries = append(entries, fileEntry{
			Name:            directoryEntry.Name(),
			Path:            entryRelative,
			ServerPath:      s.paths.serverPath(entryRelative),
			Type:            entryType,
			Size:            entryInfo.Size(),
			ModifiedAt:      entryInfo.ModTime(),
			MIME:            entryMIME,
			Preview:         entryPreview,
			PreviewTooLarge: entryPreviewTooLarge,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	writeJSON(w, http.StatusOK, fileListResponse{
		Path:       relative,
		ServerPath: s.paths.serverPath(relative),
		Entries:    entries,
	})
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("新建文件夹请求格式错误"))
		return
	}
	name, err := cleanFileName(request.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	relativeDirectory, absoluteDirectory, err := s.paths.resolveExisting(request.Path)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	info, err := os.Stat(absoluteDirectory)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("目标目录不存在"))
		return
	}

	target := filepath.Join(absoluteDirectory, name)
	if !s.paths.withinRoot(target) {
		writeError(w, http.StatusBadRequest, errInvalidPath)
		return
	}
	if err := os.Mkdir(target, 0o750); err != nil {
		if os.IsExist(err) {
			writeError(w, http.StatusConflict, errors.New("同名文件或文件夹已存在"))
			return
		}
		writeError(w, http.StatusInternalServerError, errors.New("无法新建文件夹"))
		return
	}
	relative := filepath.ToSlash(filepath.Join(relativeDirectory, name))
	writeJSON(w, http.StatusCreated, fileEntry{
		Name:       name,
		Path:       relative,
		ServerPath: s.paths.serverPath(relative),
		Type:       "directory",
		ModifiedAt: time.Now(),
	})
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	relative, absolute, err := s.paths.resolveExisting(r.URL.Query().Get("path"))
	if err != nil {
		s.writePathError(w, err)
		return
	}
	info, err := os.Stat(absolute)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, errors.New("目标不是普通文件"))
		return
	}
	file, err := os.Open(absolute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法打开文件"))
		return
	}
	defer file.Close()

	name := filepath.Base(absolute)
	contentType := mimeTypeForName(name)
	preview := previewKind(name, contentType)
	download := r.URL.Query().Get("download") == "1"
	if preview == "" {
		download = true
	}
	if preview == "text" {
		contentType = "text/plain; charset=utf-8"
	}

	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-ClawFiles-Path", url.QueryEscape(relative))
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (s *Server) writePathError(w http.ResponseWriter, err error) {
	switch {
	case os.IsNotExist(err):
		writeError(w, http.StatusNotFound, errors.New("文件或目录不存在"))
	case errors.Is(err, errInvalidPath), errors.Is(err, errSymlinkPath):
		writeError(w, http.StatusBadRequest, err)
	case os.IsPermission(err):
		writeError(w, http.StatusForbidden, errors.New("没有权限访问该路径"))
	default:
		writeError(w, http.StatusInternalServerError, errors.New("文件系统操作失败"))
	}
}

func mimeTypeForName(name string) string {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

func previewKind(name, contentType string) string {
	extension := strings.ToLower(filepath.Ext(name))
	switch {
	case extension == ".svg" || extension == ".html" || extension == ".htm":
		return "text"
	case extension == ".md" || extension == ".markdown":
		return "markdown"
	case extension == ".json":
		return "json"
	case extension == ".csv" || extension == ".tsv":
		return "table"
	case extension == ".docx":
		return "document"
	case extension == ".xlsx":
		return "spreadsheet"
	case extension == ".pptx":
		return "presentation"
	case extension == ".zip":
		return "archive"
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case contentType == "application/pdf":
		return "pdf"
	case strings.HasPrefix(contentType, "text/"):
		return "text"
	}
	switch extension {
	case ".yaml", ".yml", ".toml", ".ini", ".conf",
		".log", ".go", ".js", ".ts", ".tsx", ".jsx", ".css", ".scss", ".xml",
		".sh", ".ps1", ".py", ".java", ".rs", ".sql":
		return "text"
	default:
		return ""
	}
}

func previewKindForSize(name, contentType string, size, maxSize int64) (string, bool) {
	kind := previewKind(name, contentType)
	if kind == "" {
		return "", false
	}
	if maxSize > 0 && size > maxSize {
		return "", true
	}
	return kind, false
}
