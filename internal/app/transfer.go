package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	transferStateFileName = "stun.json"
	uploadTokenLifetime   = uploadExpiry
	contentTokenLifetime  = 5 * time.Minute
)

type transferState struct {
	IP           string    `json:"ip"`
	Port         int       `json:"port"`
	PreviousPort int       `json:"previousPort,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	ReceivedAt   time.Time `json:"receivedAt"`
}

type transferClaims struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Expires  int64  `json:"expires"`
}

type transferManager struct {
	mu           sync.RWMutex
	statePath    string
	domain       string
	webhookToken string
	signingKey   []byte
	state        transferState
}

func newTransferManager(metadataDirectory string, config Config) (*transferManager, error) {
	manager := &transferManager{
		statePath:    filepath.Join(metadataDirectory, transferStateFileName),
		domain:       config.STUNTransferDomain,
		webhookToken: config.STUNWebhookToken,
		signingKey:   []byte(config.TransferSigningKey),
	}
	if !manager.enabled() {
		return manager, nil
	}
	data, err := os.ReadFile(manager.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return manager, nil
		}
		return nil, fmt.Errorf("read STUN transfer state: %w", err)
	}
	if err := json.Unmarshal(data, &manager.state); err != nil {
		return nil, fmt.Errorf("decode STUN transfer state: %w", err)
	}
	if manager.state.Port < 1 || manager.state.Port > 65535 || net.ParseIP(manager.state.IP) == nil {
		return nil, errors.New("stored STUN transfer state is invalid")
	}
	return manager, nil
}

func (t *transferManager) enabled() bool {
	return t != nil && t.domain != "" && t.webhookToken != "" && len(t.signingKey) > 0
}

func (t *transferManager) snapshot() (transferState, string) {
	if !t.enabled() {
		return transferState{}, ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.state.Port == 0 {
		return transferState{}, ""
	}
	return t.state, fmt.Sprintf("https://%s:%d", t.domain, t.state.Port)
}

func (t *transferManager) signToken(kind, resource string, lifetime time.Duration) string {
	if !t.enabled() {
		return ""
	}
	claims := transferClaims{
		Kind:     kind,
		Resource: resource,
		Expires:  time.Now().Add(lifetime).Unix(),
	}
	payload, _ := json.Marshal(claims)
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, t.signingKey)
	_, _ = mac.Write([]byte(payloadPart))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadPart + "." + signature
}

func (t *transferManager) verifyToken(token, kind, resource string) bool {
	if !t.enabled() {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, t.signingKey)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var claims transferClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return claims.Kind == kind &&
		claims.Resource == resource &&
		claims.Expires > time.Now().Unix()
}

func (s *Server) handleSTUNWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.transfer.enabled() {
		writeError(w, http.StatusNotFound, errors.New("STUN 高速通道未启用"))
		return
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		writeError(w, http.StatusUnauthorized, errors.New("Webhook 令牌无效"))
		return
	}
	providedToken := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if providedToken == "" ||
		!hmac.Equal([]byte(providedToken), []byte(s.transfer.webhookToken)) {
		writeError(w, http.StatusUnauthorized, errors.New("Webhook 令牌无效"))
		return
	}

	var request struct {
		Event        string    `json:"event"`
		IP           string    `json:"ip"`
		Port         int       `json:"port"`
		PreviousPort int       `json:"previous_port"`
		UpdatedAt    time.Time `json:"updated_at"`
	}
	if err := decodeJSONAllowUnknown(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("Webhook JSON 格式错误: %w", err))
		return
	}
	request.Event = strings.TrimSpace(request.Event)
	request.IP = strings.TrimSpace(request.IP)
	if request.Event == "test" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"event": "test",
		})
		return
	}
	if request.Event != "stun_port_changed" {
		writeError(w, http.StatusBadRequest, errors.New("不支持的 Webhook 事件"))
		return
	}
	if net.ParseIP(request.IP) == nil {
		writeError(w, http.StatusBadRequest, errors.New("公网 IP 无效"))
		return
	}
	if request.Port < 1 || request.Port > 65535 {
		writeError(w, http.StatusBadRequest, errors.New("STUN 端口无效"))
		return
	}
	if request.UpdatedAt.IsZero() {
		writeError(w, http.StatusBadRequest, errors.New("updated_at 无效"))
		return
	}

	state := transferState{
		IP:           request.IP,
		Port:         request.Port,
		PreviousPort: request.PreviousPort,
		UpdatedAt:    request.UpdatedAt.UTC(),
		ReceivedAt:   time.Now().UTC(),
	}
	s.transfer.mu.Lock()
	accepted := s.transfer.state.UpdatedAt.IsZero() ||
		state.UpdatedAt.After(s.transfer.state.UpdatedAt)
	if accepted {
		if err := writeJSONFileAtomic(s.transfer.statePath, state, 0o600); err != nil {
			s.transfer.mu.Unlock()
			writeError(w, http.StatusInternalServerError, errors.New("无法保存 STUN 端口"))
			return
		}
		s.transfer.state = state
	}
	current := s.transfer.state
	endpoint := ""
	if current.Port != 0 {
		endpoint = fmt.Sprintf("https://%s:%d", s.transfer.domain, current.Port)
	}
	s.transfer.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":  accepted,
		"endpoint":  endpoint,
		"updatedAt": current.UpdatedAt,
	})
}

func (s *Server) handleGetTransfer(w http.ResponseWriter, _ *http.Request) {
	state, endpoint := s.transfer.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   s.transfer.enabled(),
		"available": endpoint != "",
		"baseUrl":   endpoint,
		"updatedAt": state.UpdatedAt,
	})
}

func (s *Server) handleTransferContentPlan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path     string `json:"path"`
		Download bool   `json:"download"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("下载请求格式错误"))
		return
	}
	relative, absolute, err := s.paths.resolveExisting(request.Path)
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

	query := url.Values{"path": []string{relative}}
	if request.Download {
		query.Set("download", "1")
	}
	fallbackURL := "/api/content?" + query.Encode()
	_, endpoint := s.transfer.snapshot()
	directURL := ""
	if endpoint != "" {
		query.Set("transfer_token", s.transfer.signToken("content", relative, contentTokenLifetime))
		directURL = endpoint + "/api/content?" + query.Encode()
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"directUrl":   directURL,
		"fallbackUrl": fallbackURL,
	})
}

func (s *Server) attachUploadTransferToken(status *uploadStatus) {
	if status == nil || !s.transfer.enabled() {
		return
	}
	status.TransferToken = s.transfer.signToken("upload", status.ID, uploadTokenLifetime)
}

func (s *Server) authorizeTransferRequest(r *http.Request) bool {
	if !s.transfer.enabled() {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/uploads/") &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodPatch) {
		id := strings.TrimPrefix(r.URL.Path, "/api/uploads/")
		return validUploadID(id) &&
			s.transfer.verifyToken(r.Header.Get("X-ClawFiles-Transfer-Token"), "upload", id)
	}
	if r.URL.Path == "/api/content" &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		relative, _, err := s.paths.resolve(r.URL.Query().Get("path"))
		return err == nil &&
			s.transfer.verifyToken(r.URL.Query().Get("transfer_token"), "content", relative)
	}
	if strings.HasPrefix(r.URL.Path, "/api/selection/archive/") &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		id := strings.TrimPrefix(r.URL.Path, "/api/selection/archive/")
		return validUploadID(id) &&
			s.transfer.verifyToken(r.URL.Query().Get("transfer_token"), "archive", id)
	}
	return false
}

func (s *Server) transferCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isTransferResource(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !validTransferOrigin(origin) {
			writeError(w, http.StatusForbidden, errors.New("跨域来源无效"))
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Accept, Content-Type, Range, Upload-Offset, Upload-Chunk-Length, X-ClawFiles-Request, X-ClawFiles-Transfer-Token")
		w.Header().Set("Access-Control-Expose-Headers",
			"Accept-Ranges, Content-Disposition, Content-Length, Content-Range, Upload-Length, Upload-Offset")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isTransferResource(path string) bool {
	return path == "/api/content" ||
		strings.HasPrefix(path, "/api/uploads/") ||
		strings.HasPrefix(path, "/api/selection/archive/")
}

func validTransferOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
