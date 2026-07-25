package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxRecentEntries = 200

type recentEntry struct {
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	ServerPath      string    `json:"serverPath"`
	Size            int64     `json:"size"`
	ModifiedAt      time.Time `json:"modifiedAt"`
	UploadedAt      time.Time `json:"uploadedAt"`
	MIME            string    `json:"mime,omitempty"`
	Preview         string    `json:"preview,omitempty"`
	PreviewTooLarge bool      `json:"previewTooLarge,omitempty"`
}

type recentStore struct {
	mu             sync.Mutex
	filePath       string
	paths          pathResolver
	maxPreviewSize int64
}

func newRecentStore(metadataDirectory string, paths pathResolver, maxPreviewSize int64) *recentStore {
	return &recentStore{
		filePath:       filepath.Join(metadataDirectory, "recent.json"),
		paths:          paths,
		maxPreviewSize: maxPreviewSize,
	}
}

func (r *recentStore) add(entry recentEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.readUnlocked()
	if err != nil {
		return err
	}
	filtered := make([]recentEntry, 0, len(entries)+1)
	filtered = append(filtered, entry)
	for _, current := range entries {
		if current.Path != entry.Path {
			filtered = append(filtered, current)
		}
		if len(filtered) >= maxRecentEntries {
			break
		}
	}
	return writeJSONFileAtomic(r.filePath, filtered, 0o600)
}

func (r *recentStore) list(limit int) ([]recentEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.readUnlocked()
	if err != nil {
		return nil, err
	}
	valid := make([]recentEntry, 0, len(entries))
	for _, entry := range entries {
		relative, absolute, err := r.paths.resolveExisting(entry.Path)
		if err != nil {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		entry.Path = relative
		entry.ServerPath = r.paths.serverPath(relative)
		entry.Name = filepath.Base(absolute)
		entry.Size = info.Size()
		entry.ModifiedAt = info.ModTime()
		entry.MIME = mimeTypeForName(entry.Name)
		entry.Preview, entry.PreviewTooLarge = previewKindForSize(
			entry.Name,
			entry.MIME,
			entry.Size,
			r.maxPreviewSize,
		)
		valid = append(valid, entry)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		return valid[i].UploadedAt.After(valid[j].UploadedAt)
	})
	if limit > 0 && len(valid) > limit {
		valid = valid[:limit]
	}
	return valid, nil
}

func (r *recentStore) remapPaths(remaps []pathRemap) error {
	if len(remaps) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.readUnlocked()
	if err != nil {
		return err
	}
	changed := false
	for index := range entries {
		for _, remap := range remaps {
			if entries[index].Path != remap.From &&
				!strings.HasPrefix(entries[index].Path, remap.From+"/") {
				continue
			}
			suffix := strings.TrimPrefix(entries[index].Path, remap.From)
			entries[index].Path = strings.TrimPrefix(path.Clean("/"+remap.To+suffix), "/")
			entries[index].ServerPath = r.paths.serverPath(entries[index].Path)
			entries[index].Name = path.Base(entries[index].Path)
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return writeJSONFileAtomic(r.filePath, entries, 0o600)
}

func (r *recentStore) readUnlocked() ([]recentEntry, error) {
	data, err := os.ReadFile(r.filePath)
	if os.IsNotExist(err) {
		return []recentEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []recentEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	limit := 50
	entries, err := s.recent.list(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法读取最近上传记录"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func writeJSONFileAtomic(filePath string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tempPath := filePath + ".tmp"
	if err := os.WriteFile(tempPath, data, mode); err != nil {
		return err
	}
	return os.Rename(tempPath, filePath)
}
