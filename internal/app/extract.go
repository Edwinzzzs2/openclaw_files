package app

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxExtractFiles = 10000

func (s *Server) handleExtractSelection(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Directory string `json:"directory"`
		Path      string `json:"path"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("解压请求格式错误"))
		return
	}
	targets, err := s.resolveSelectionTargets(request.Directory, []string{request.Path})
	if err != nil {
		s.writeSelectionError(w, err)
		return
	}
	target := targets[0]
	if !target.info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(target.info.Name()), ".zip") {
		writeError(w, http.StatusBadRequest, errors.New("当前仅支持解压 ZIP 文件"))
		return
	}

	reader, err := zip.OpenReader(target.absolute)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, errors.New("ZIP 文件格式无效或已损坏"))
		return
	}
	defer reader.Close()
	if len(reader.File) > maxExtractFiles {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Errorf("压缩包内文件过多，一次最多解压 %d 项", maxExtractFiles))
		return
	}

	_, directoryAbsolute, err := s.paths.resolveExisting(request.Directory)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	baseName := strings.TrimSuffix(target.info.Name(), filepath.Ext(target.info.Name()))
	outputName, outputRelative, outputAbsolute, err := createExtractDirectory(
		directoryAbsolute,
		request.Directory,
		baseName,
	)
	if err != nil {
		if os.IsPermission(err) {
			writeError(w, http.StatusForbidden, errors.New("没有权限创建解压目录"))
			return
		}
		writeError(w, http.StatusInternalServerError, errors.New("无法创建解压目录"))
		return
	}

	fileCount, totalSize, extractErr := extractZIPFiles(reader.File, outputAbsolute, s.config.MaxUploadSize)
	if extractErr != nil {
		_ = os.RemoveAll(outputAbsolute)
		switch {
		case errors.Is(extractErr, errInvalidPath):
			writeError(w, http.StatusBadRequest, errors.New("压缩包包含不安全路径，已停止解压"))
		case errors.Is(extractErr, errSymlinkPath):
			writeError(w, http.StatusBadRequest, errors.New("压缩包包含符号链接，已停止解压"))
		case errors.Is(extractErr, errExtractTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Errorf("解压后内容超过 %s，已停止解压", formatByteSize(s.config.MaxUploadSize)))
		default:
			writeError(w, http.StatusUnprocessableEntity, errors.New("解压失败，压缩包可能已损坏"))
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"name":       outputName,
		"path":       outputRelative,
		"serverPath": s.paths.serverPath(outputRelative),
		"files":      fileCount,
		"size":       totalSize,
	})
}

var errExtractTooLarge = errors.New("extracted content exceeds limit")

func createExtractDirectory(
	directoryAbsolute string,
	directoryRelative string,
	baseName string,
) (string, string, string, error) {
	baseName = strings.TrimSpace(baseName)
	if _, err := cleanFileName(baseName); err != nil {
		baseName = "解压文件"
	}
	for index := 0; index < 10000; index++ {
		name := baseName
		if index > 0 {
			name = fmt.Sprintf("%s (%d)", baseName, index)
		}
		absolute := filepath.Join(directoryAbsolute, name)
		err := os.Mkdir(absolute, 0o750)
		if err == nil {
			relative := filepath.ToSlash(filepath.Join(directoryRelative, name))
			return name, relative, absolute, nil
		}
		if !os.IsExist(err) {
			return "", "", "", err
		}
	}
	return "", "", "", errors.New("too many matching extraction directories")
}

func extractZIPFiles(files []*zip.File, outputDirectory string, maxBytes int64) (int, int64, error) {
	var totalSize int64
	fileCount := 0
	seen := make(map[string]struct{}, len(files))
	for _, archiveFile := range files {
		cleanName, err := safeArchiveName(archiveFile.Name)
		if err != nil {
			return 0, 0, err
		}
		if cleanName == "" {
			continue
		}
		if archiveFile.Mode()&os.ModeSymlink != 0 {
			return 0, 0, errSymlinkPath
		}
		key := strings.ToLower(cleanName)
		if _, exists := seen[key]; exists {
			return 0, 0, errors.New("duplicate archive path")
		}
		seen[key] = struct{}{}

		target := filepath.Join(outputDirectory, filepath.FromSlash(cleanName))
		relative, err := filepath.Rel(outputDirectory, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return 0, 0, errInvalidPath
		}
		if archiveFile.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return 0, 0, err
			}
			continue
		}
		if !archiveFile.Mode().IsRegular() {
			return 0, 0, errors.New("unsupported archive entry")
		}
		if archiveFile.UncompressedSize64 > uint64(maxBytes) ||
			totalSize > maxBytes-int64(archiveFile.UncompressedSize64) {
			return 0, 0, errExtractTooLarge
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return 0, 0, err
		}
		written, err := extractZIPFile(archiveFile, target, maxBytes-totalSize)
		if err != nil {
			return 0, 0, err
		}
		totalSize += written
		fileCount++
	}
	return fileCount, totalSize, nil
}

func safeArchiveName(name string) (string, error) {
	normalized := strings.ReplaceAll(name, "\\", "/")
	if strings.ContainsRune(normalized, 0) || strings.HasPrefix(normalized, "/") {
		return "", errInvalidPath
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", errInvalidPath
		}
	}
	cleaned := strings.TrimSuffix(path.Clean(normalized), "/")
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errInvalidPath
	}
	return cleaned, nil
}

func extractZIPFile(archiveFile *zip.File, target string, maxBytes int64) (int64, error) {
	source, err := archiveFile.Open()
	if err != nil {
		return 0, err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(destination, io.LimitReader(source, maxBytes+1))
	closeErr := destination.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if written > maxBytes {
		return 0, errExtractTooLarge
	}
	return written, nil
}
