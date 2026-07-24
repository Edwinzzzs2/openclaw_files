package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	metadataDirectoryName = ".clawfiles"
	uploadExpiry          = 7 * 24 * time.Hour
)

type uploadMetadata struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Directory    string    `json:"directory"`
	Size         int64     `json:"size"`
	Offset       int64     `json:"offset"`
	LastModified int64     `json:"lastModified,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type uploadStatus struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Directory     string       `json:"directory"`
	Size          int64        `json:"size"`
	Offset        int64        `json:"offset"`
	ChunkSize     int64        `json:"chunkSize"`
	Completed     bool         `json:"completed"`
	File          *recentEntry `json:"file,omitempty"`
	TransferToken string       `json:"transferToken,omitempty"`
}

type uploadManager struct {
	directory string
	paths     pathResolver
	recent    *recentStore
	maxSize   int64
	chunkSize int64
	locks     sync.Map
}

func newUploadManager(
	directory string,
	paths pathResolver,
	recent *recentStore,
	maxSize int64,
	chunkSize int64,
) *uploadManager {
	manager := &uploadManager{
		directory: directory,
		paths:     paths,
		recent:    recent,
		maxSize:   maxSize,
		chunkSize: chunkSize,
	}
	manager.cleanupExpired()
	return manager
}

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Directory    string `json:"directory"`
		Name         string `json:"name"`
		Size         int64  `json:"size"`
		LastModified int64  `json:"lastModified,omitempty"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("上传请求格式错误"))
		return
	}
	name, err := cleanFileName(request.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Size < 0 {
		writeError(w, http.StatusBadRequest, errors.New("文件大小无效"))
		return
	}
	if request.Size > s.uploads.maxSize {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("文件超过上传上限"))
		return
	}
	relativeDirectory, absoluteDirectory, err := s.paths.resolveExisting(request.Directory)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	directoryInfo, err := os.Stat(absoluteDirectory)
	if err != nil || !directoryInfo.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("上传目录不存在"))
		return
	}

	id, err := randomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法创建上传任务"))
		return
	}
	now := time.Now().UTC()
	metadata := uploadMetadata{
		ID:           id,
		Name:         name,
		Directory:    relativeDirectory,
		Size:         request.Size,
		LastModified: request.LastModified,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	partFile, err := os.OpenFile(s.uploads.partPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法创建临时上传文件"))
		return
	}
	if err := partFile.Close(); err != nil {
		_ = os.Remove(s.uploads.partPath(id))
		writeError(w, http.StatusInternalServerError, errors.New("无法初始化上传任务"))
		return
	}
	if err := s.uploads.save(metadata); err != nil {
		_ = os.Remove(s.uploads.partPath(id))
		writeError(w, http.StatusInternalServerError, errors.New("无法保存上传任务"))
		return
	}

	status := uploadStatusFromMetadata(metadata, s.uploads.chunkSize)
	w.Header().Set("Location", "/api/uploads/"+id)
	w.Header().Set("Upload-Offset", "0")
	w.Header().Set("Upload-Length", strconv.FormatInt(request.Size, 10))

	if request.Size == 0 {
		file, err := s.uploads.complete(metadata)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("无法保存空文件"))
			return
		}
		status.Completed = true
		status.File = &file
	}
	s.attachUploadTransferToken(&status)
	writeJSON(w, http.StatusCreated, status)
}

func (s *Server) handleGetUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUploadID(id) {
		writeError(w, http.StatusNotFound, errors.New("上传任务不存在"))
		return
	}
	unlock := s.uploads.lock(id)
	defer unlock()

	metadata, err := s.uploads.load(id)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, errors.New("上传任务不存在"))
			return
		}
		writeError(w, http.StatusInternalServerError, errors.New("无法读取上传任务"))
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(metadata.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(metadata.Size, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	status := uploadStatusFromMetadata(metadata, s.uploads.chunkSize)
	s.attachUploadTransferToken(&status)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handlePatchUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUploadID(id) {
		writeError(w, http.StatusNotFound, errors.New("上传任务不存在"))
		return
	}
	if contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); contentType != "application/offset+octet-stream" {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("上传分块格式错误"))
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, errors.New("上传偏移量无效"))
		return
	}
	declaredChunkLength := r.ContentLength
	if headerValue := strings.TrimSpace(r.Header.Get("Upload-Chunk-Length")); headerValue != "" {
		headerLength, err := strconv.ParseInt(headerValue, 10, 64)
		if err != nil || headerLength <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("上传分块长度无效"))
			return
		}
		if r.ContentLength > 0 && r.ContentLength != headerLength {
			writeError(w, http.StatusBadRequest, errors.New("上传分块长度不一致"))
			return
		}
		declaredChunkLength = headerLength
	}
	if declaredChunkLength <= 0 {
		writeError(w, http.StatusLengthRequired, errors.New("上传分块不能为空"))
		return
	}

	unlock := s.uploads.lock(id)
	defer unlock()

	metadata, err := s.uploads.load(id)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, errors.New("上传任务不存在"))
			return
		}
		writeError(w, http.StatusInternalServerError, errors.New("无法读取上传任务"))
		return
	}
	if metadata.Offset != offset {
		w.Header().Set("Upload-Offset", strconv.FormatInt(metadata.Offset, 10))
		writeError(w, http.StatusConflict, errors.New("上传偏移量已变化，请从服务器进度继续"))
		return
	}
	remaining := metadata.Size - metadata.Offset
	if remaining <= 0 {
		writeError(w, http.StatusConflict, errors.New("上传任务已经完成"))
		return
	}
	maxChunkLength := min(s.uploads.chunkSize, remaining)
	if declaredChunkLength > maxChunkLength {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("上传分块超过允许大小"))
		return
	}

	file, err := os.OpenFile(s.uploads.partPath(id), os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法打开临时上传文件"))
		return
	}
	if _, err := file.Seek(metadata.Offset, io.SeekStart); err != nil {
		_ = file.Close()
		writeError(w, http.StatusInternalServerError, errors.New("无法定位上传偏移量"))
		return
	}
	// Stop as soon as the declared chunk is complete. Some mobile WebViews and
	// reverse proxies delay the final request-body EOF even after all bytes have
	// arrived, which otherwise leaves a completed chunk waiting indefinitely.
	written, copyErr := io.CopyN(file, r.Body, declaredChunkLength)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || written != declaredChunkLength || syncErr != nil || closeErr != nil {
		_ = os.Truncate(s.uploads.partPath(id), metadata.Offset)
		writeError(w, http.StatusBadRequest, errors.New("上传分块不完整，请重试"))
		return
	}

	metadata.Offset += written
	metadata.UpdatedAt = time.Now().UTC()
	if err := s.uploads.save(metadata); err != nil {
		_ = os.Truncate(s.uploads.partPath(id), offset)
		writeError(w, http.StatusInternalServerError, errors.New("无法保存上传进度"))
		return
	}

	status := uploadStatusFromMetadata(metadata, s.uploads.chunkSize)
	w.Header().Set("Upload-Offset", strconv.FormatInt(metadata.Offset, 10))
	if metadata.Offset == metadata.Size {
		completedFile, err := s.uploads.complete(metadata)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("上传完成但文件落盘失败"))
			return
		}
		status.Completed = true
		status.File = &completedFile
	}
	s.attachUploadTransferToken(&status)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUploadID(id) {
		writeError(w, http.StatusNotFound, errors.New("上传任务不存在"))
		return
	}
	unlock := s.uploads.lock(id)
	defer unlock()

	metadataErr := os.Remove(s.uploads.metadataPath(id))
	partErr := os.Remove(s.uploads.partPath(id))
	if os.IsNotExist(metadataErr) && os.IsNotExist(partErr) {
		writeError(w, http.StatusNotFound, errors.New("上传任务不存在"))
		return
	}
	if metadataErr != nil && !os.IsNotExist(metadataErr) {
		writeError(w, http.StatusInternalServerError, errors.New("无法取消上传任务"))
		return
	}
	if partErr != nil && !os.IsNotExist(partErr) {
		writeError(w, http.StatusInternalServerError, errors.New("无法清理临时上传文件"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (u *uploadManager) complete(metadata uploadMetadata) (recentEntry, error) {
	relativeDirectory, absoluteDirectory, err := u.paths.resolveExisting(metadata.Directory)
	if err != nil {
		return recentEntry{}, err
	}
	info, err := os.Stat(absoluteDirectory)
	if err != nil || !info.IsDir() {
		return recentEntry{}, errors.New("上传目标目录不存在")
	}

	source := u.partPath(metadata.ID)
	finalName, finalPath, err := u.linkWithAvailableName(source, absoluteDirectory, metadata.Name)
	if err != nil {
		return recentEntry{}, err
	}
	if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
		log.Printf("remove completed upload temp file %s: %v", metadata.ID, err)
	}
	if err := os.Remove(u.metadataPath(metadata.ID)); err != nil && !os.IsNotExist(err) {
		log.Printf("remove completed upload metadata %s: %v", metadata.ID, err)
	}

	info, err = os.Stat(finalPath)
	if err != nil {
		return recentEntry{}, err
	}
	relative := filepath.ToSlash(filepath.Join(relativeDirectory, finalName))
	entry := recentEntry{
		Name:       finalName,
		Path:       relative,
		ServerPath: u.paths.serverPath(relative),
		Size:       info.Size(),
		ModifiedAt: info.ModTime(),
		UploadedAt: time.Now().UTC(),
		MIME:       mimeTypeForName(finalName),
	}
	entry.Preview = previewKind(finalName, entry.MIME)
	if err := u.recent.add(entry); err != nil {
		log.Printf("save recent upload %s: %v", relative, err)
	}
	return entry, nil
}

func (u *uploadManager) linkWithAvailableName(source, directory, originalName string) (string, string, error) {
	extension := filepath.Ext(originalName)
	base := strings.TrimSuffix(originalName, extension)
	for index := 0; index < 10000; index++ {
		name := originalName
		if index > 0 {
			name = fmt.Sprintf("%s (%d)%s", base, index, extension)
		}
		target := filepath.Join(directory, name)
		if !u.paths.withinRoot(target) {
			return "", "", errInvalidPath
		}
		err := os.Link(source, target)
		if err == nil {
			return name, target, nil
		}
		if os.IsExist(err) {
			continue
		}
		return "", "", err
	}
	return "", "", errors.New("同名文件过多")
}

func (u *uploadManager) save(metadata uploadMetadata) error {
	return writeJSONFileAtomic(u.metadataPath(metadata.ID), metadata, 0o600)
}

func (u *uploadManager) load(id string) (uploadMetadata, error) {
	var metadata uploadMetadata
	data, err := os.ReadFile(u.metadataPath(id))
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return metadata, err
	}
	if metadata.ID != id {
		return metadata, errors.New("上传任务元数据不匹配")
	}
	return metadata, nil
}

func (u *uploadManager) metadataPath(id string) string {
	return filepath.Join(u.directory, id+".json")
}

func (u *uploadManager) partPath(id string) string {
	return filepath.Join(u.directory, id+".part")
}

func (u *uploadManager) lock(id string) func() {
	value, _ := u.locks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return func() {
		mutex.Unlock()
	}
}

func (u *uploadManager) cleanupExpired() {
	entries, err := os.ReadDir(u.directory)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-uploadExpiry)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validUploadID(id) {
			continue
		}
		metadata, err := u.load(id)
		if err != nil || metadata.UpdatedAt.Before(cutoff) {
			_ = os.Remove(u.metadataPath(id))
			_ = os.Remove(u.partPath(id))
		}
	}
}

func uploadStatusFromMetadata(metadata uploadMetadata, chunkSize int64) uploadStatus {
	return uploadStatus{
		ID:        metadata.ID,
		Name:      metadata.Name,
		Directory: metadata.Directory,
		Size:      metadata.Size,
		Offset:    metadata.Offset,
		ChunkSize: chunkSize,
	}
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validUploadID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
