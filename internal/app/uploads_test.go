package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type failAfterDataReader struct {
	data []byte
}

func (r *failAfterDataReader) Read(buffer []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("server read past declared chunk length")
	}
	count := copy(buffer, r.data)
	r.data = r.data[count:]
	return count, nil
}

func TestUploadAcceptsUnknownContentLength(t *testing.T) {
	storageRoot := t.TempDir()
	handler, err := NewServer(Config{
		StorageRoot:     storageRoot,
		HostPathPrefix:  storageRoot,
		MaxUploadSize:   1024 * 1024,
		UploadChunkSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	createBody := bytes.NewBufferString(`{"directory":"","name":"mobile.txt","size":6}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/uploads", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("X-ClawFiles-Request", "1")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create upload returned %d: %s", createResponse.Code, createResponse.Body.String())
	}

	var upload uploadStatus
	if err := json.Unmarshal(createResponse.Body.Bytes(), &upload); err != nil {
		t.Fatalf("decode create upload response: %v", err)
	}

	chunkRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/uploads/"+upload.ID,
		&failAfterDataReader{data: []byte("mobile")},
	)
	chunkRequest.ContentLength = -1
	chunkRequest.TransferEncoding = []string{"chunked"}
	chunkRequest.Header.Set("Content-Type", "application/offset+octet-stream")
	chunkRequest.Header.Set("Upload-Offset", "0")
	chunkRequest.Header.Set("Upload-Chunk-Length", "6")
	chunkRequest.Header.Set("X-ClawFiles-Request", "1")
	chunkResponse := httptest.NewRecorder()
	handler.ServeHTTP(chunkResponse, chunkRequest)
	if chunkResponse.Code != http.StatusOK {
		t.Fatalf("upload chunk returned %d: %s", chunkResponse.Code, chunkResponse.Body.String())
	}

	content, err := os.ReadFile(filepath.Join(storageRoot, "mobile.txt"))
	if err != nil {
		t.Fatalf("read completed upload: %v", err)
	}
	if string(content) != "mobile" {
		t.Fatalf("completed upload contains %q", content)
	}
}
