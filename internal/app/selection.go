package app

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxSelectionItems     = 500
	maxArchiveTickets     = 128
	archiveTicketLifetime = 5 * time.Minute
)

type selectionRequest struct {
	Directory string   `json:"directory"`
	Paths     []string `json:"paths"`
}

type selectionTarget struct {
	relative string
	absolute string
	info     os.FileInfo
}

type archiveTicket struct {
	directory string
	paths     []string
	expiresAt time.Time
}

type archiveTicketStore struct {
	mu      sync.Mutex
	tickets map[string]archiveTicket
}

func newArchiveTicketStore() *archiveTicketStore {
	return &archiveTicketStore{tickets: make(map[string]archiveTicket)}
}

func (s *archiveTicketStore) create(directory string, paths []string) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for ticketID, ticket := range s.tickets {
		if !ticket.expiresAt.After(now) {
			delete(s.tickets, ticketID)
		}
	}
	if len(s.tickets) >= maxArchiveTickets {
		var oldestID string
		var oldestExpiry time.Time
		for ticketID, ticket := range s.tickets {
			if oldestID == "" || ticket.expiresAt.Before(oldestExpiry) {
				oldestID = ticketID
				oldestExpiry = ticket.expiresAt
			}
		}
		delete(s.tickets, oldestID)
	}
	s.tickets[id] = archiveTicket{
		directory: directory,
		paths:     append([]string(nil), paths...),
		expiresAt: now.Add(archiveTicketLifetime),
	}
	return id, nil
}

func (s *archiveTicketStore) get(id string) (archiveTicket, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[id]
	if !ok || !ticket.expiresAt.After(now) {
		delete(s.tickets, id)
		return archiveTicket{}, false
	}
	ticket.paths = append([]string(nil), ticket.paths...)
	return ticket, true
}

func (s *Server) handlePrepareSelectionArchive(w http.ResponseWriter, r *http.Request) {
	var request selectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("下载选择格式错误"))
		return
	}
	targets, err := s.resolveSelectionTargets(request.Directory, request.Paths)
	if err != nil {
		s.writeSelectionError(w, err)
		return
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.relative)
	}
	id, err := s.archiveTickets.create(request.Directory, paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法准备下载"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"url": "/selection/archive/" + id,
	})
}

func (s *Server) handleDownloadSelectionArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUploadID(id) {
		writeError(w, http.StatusNotFound, errors.New("下载任务不存在或已过期"))
		return
	}
	ticket, ok := s.archiveTickets.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("下载任务不存在或已过期"))
		return
	}
	targets, err := s.resolveSelectionTargets(ticket.directory, ticket.paths)
	if err != nil {
		s.writeSelectionError(w, err)
		return
	}

	name := "clawfiles-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(name)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")

	writer := zip.NewWriter(w)
	for _, target := range targets {
		if err := s.writeArchiveTarget(writer, target); err != nil {
			log.Printf("stream selection archive: %v", err)
			_ = writer.Close()
			return
		}
	}
	if err := writer.Close(); err != nil {
		log.Printf("close selection archive: %v", err)
	}
}

func (s *Server) handleSelectionArchivePlan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(w, r, &request); err != nil || !validUploadID(request.ID) {
		writeError(w, http.StatusBadRequest, errors.New("下载任务无效"))
		return
	}
	if _, ok := s.archiveTickets.get(request.ID); !ok {
		writeError(w, http.StatusNotFound, errors.New("下载任务不存在或已过期"))
		return
	}
	fallbackURL := "/api/selection/archive/" + request.ID
	directURL := ""
	if _, endpoint := s.transfer.snapshot(); endpoint != "" {
		token := s.transfer.signToken("archive", request.ID, contentTokenLifetime)
		directURL = endpoint + fallbackURL + "?transfer_token=" + url.QueryEscape(token)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"directUrl":   directURL,
		"fallbackUrl": fallbackURL,
	})
}

func (s *Server) handleDeleteSelection(w http.ResponseWriter, r *http.Request) {
	var request selectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("删除选择格式错误"))
		return
	}
	targets, err := s.resolveSelectionTargets(request.Directory, request.Paths)
	if err != nil {
		s.writeSelectionError(w, err)
		return
	}
	deleted := make([]string, 0, len(targets))
	for _, target := range targets {
		relative, absolute, err := s.paths.resolveExisting(target.relative)
		if err != nil {
			s.writeSelectionError(w, err)
			return
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			s.writeSelectionError(w, err)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			s.writeSelectionError(w, errSymlinkPath)
			return
		}
		if err := os.RemoveAll(absolute); err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("无法删除所选文件"))
			return
		}
		deleted = append(deleted, relative)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (s *Server) resolveSelectionTargets(rawDirectory string, rawPaths []string) ([]selectionTarget, error) {
	if len(rawPaths) == 0 {
		return nil, errors.New("请至少选择一个文件或文件夹")
	}
	if len(rawPaths) > maxSelectionItems {
		return nil, fmt.Errorf("一次最多选择 %d 项", maxSelectionItems)
	}
	directory, absoluteDirectory, err := s.paths.resolveExisting(rawDirectory)
	if err != nil {
		return nil, err
	}
	directoryInfo, err := os.Stat(absoluteDirectory)
	if err != nil {
		return nil, err
	}
	if !directoryInfo.IsDir() {
		return nil, errors.New("选择目录不存在")
	}

	byPath := make(map[string]selectionTarget, len(rawPaths))
	for _, rawPath := range rawPaths {
		relative, absolute, err := s.paths.resolveExisting(rawPath)
		if err != nil {
			return nil, err
		}
		if relative == "" {
			return nil, errors.New("不能选择根目录")
		}
		parent := strings.TrimPrefix(path.Dir("/"+relative), "/")
		if parent == "." {
			parent = ""
		}
		if parent != directory {
			return nil, errors.New("只能操作当前目录中的项目")
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, errors.New("只能选择普通文件或文件夹")
		}
		byPath[relative] = selectionTarget{
			relative: relative,
			absolute: absolute,
			info:     info,
		}
	}

	targets := make([]selectionTarget, 0, len(byPath))
	for _, target := range byPath {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if len(targets[i].relative) != len(targets[j].relative) {
			return len(targets[i].relative) < len(targets[j].relative)
		}
		return targets[i].relative < targets[j].relative
	})

	filtered := make([]selectionTarget, 0, len(targets))
	for _, target := range targets {
		covered := false
		for _, parent := range filtered {
			if strings.HasPrefix(target.relative, parent.relative+"/") {
				covered = true
				break
			}
		}
		if !covered {
			filtered = append(filtered, target)
		}
	}
	return filtered, nil
}

func (s *Server) writeArchiveTarget(writer *zip.Writer, target selectionTarget) error {
	archiveRoot := path.Base(target.relative)
	if target.info.Mode().IsRegular() {
		return writeArchiveFile(writer, target.absolute, archiveRoot, target.info)
	}
	return filepath.WalkDir(target.absolute, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !s.paths.withinRoot(currentPath) {
			return errInvalidPath
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		relativeToTarget, err := filepath.Rel(target.absolute, currentPath)
		if err != nil {
			return err
		}
		archiveName := archiveRoot
		if relativeToTarget != "." {
			archiveName = filepath.ToSlash(filepath.Join(archiveRoot, relativeToTarget))
		}
		if info.IsDir() {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = strings.TrimSuffix(filepath.ToSlash(archiveName), "/") + "/"
			_, err = writer.CreateHeader(header)
			return err
		}
		return writeArchiveFile(writer, currentPath, archiveName, info)
	})
}

func writeArchiveFile(writer *zip.Writer, absolutePath, archiveName string, info os.FileInfo) error {
	file, err := os.Open(absolutePath)
	if err != nil {
		return err
	}
	defer file.Close()

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(archiveName)
	header.Method = zip.Store
	target, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, file)
	return err
}

func (s *Server) writeSelectionError(w http.ResponseWriter, err error) {
	switch {
	case os.IsNotExist(err):
		writeError(w, http.StatusNotFound, errors.New("所选文件或文件夹不存在"))
	case errors.Is(err, errInvalidPath), errors.Is(err, errSymlinkPath):
		writeError(w, http.StatusBadRequest, err)
	case os.IsPermission(err):
		writeError(w, http.StatusForbidden, errors.New("没有权限操作所选文件"))
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
