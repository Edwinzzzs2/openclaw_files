package app

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"clawfiles/internal/webassets"
)

type Server struct {
	config         Config
	paths          pathResolver
	auth           *authenticator
	recent         *recentStore
	uploads        *uploadManager
	transfer       *transferManager
	archiveTickets *archiveTicketStore
	handler        http.Handler
}

func NewServer(config Config) (http.Handler, error) {
	metadataDirectory := filepath.Join(config.StorageRoot, metadataDirectoryName)
	uploadDirectory := filepath.Join(metadataDirectory, "uploads")
	if err := os.MkdirAll(uploadDirectory, 0o700); err != nil {
		return nil, err
	}

	paths := newPathResolver(config.StorageRoot, config.HostPathPrefix)
	recent := newRecentStore(metadataDirectory, paths)
	server := &Server{
		config:         config,
		paths:          paths,
		auth:           newAuthenticator(config.Password, config.CookieSecure),
		recent:         recent,
		archiveTickets: newArchiveTicketStore(),
	}
	transfer, err := newTransferManager(metadataDirectory, config)
	if err != nil {
		return nil, err
	}
	server.transfer = transfer
	server.uploads = newUploadManager(
		uploadDirectory,
		paths,
		recent,
		config.MaxUploadSize,
		config.UploadChunkSize,
	)
	server.handler = server.routes()
	return server, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{
			"authenticated": s.auth.authenticated(r),
			"authRequired":  s.auth.enabled(),
		})
	})
	mux.HandleFunc("POST /api/auth/login", s.auth.login)
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, _ *http.Request) {
		s.auth.logout(w)
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"hostPathPrefix": s.config.HostPathPrefix,
			"maxUploadSize":  s.config.MaxUploadSize,
			"chunkSize":      s.config.UploadChunkSize,
		})
	})

	mux.HandleFunc("GET /api/files", s.handleListFiles)
	mux.HandleFunc("POST /api/selection/delete", s.handleDeleteSelection)
	mux.HandleFunc("POST /api/folders", s.handleCreateFolder)
	mux.HandleFunc("GET /api/content", s.handleContent)
	mux.HandleFunc("HEAD /api/content", s.handleContent)
	mux.HandleFunc("GET /api/recent", s.handleRecent)
	mux.HandleFunc("GET /api/transfer", s.handleGetTransfer)
	mux.HandleFunc("POST /api/transfer/content", s.handleTransferContentPlan)
	mux.HandleFunc("POST /api/webhooks/stun", s.handleSTUNWebhook)
	mux.HandleFunc("POST /api/selection/archive", s.handlePrepareSelectionArchive)
	mux.HandleFunc("POST /api/selection/archive/plan", s.handleSelectionArchivePlan)
	mux.HandleFunc("GET /api/selection/archive/{id}", s.handleDownloadSelectionArchive)

	mux.HandleFunc("POST /api/uploads", s.handleCreateUpload)
	mux.HandleFunc("GET /api/uploads/{id}", s.handleGetUpload)
	mux.HandleFunc("HEAD /api/uploads/{id}", s.handleGetUpload)
	mux.HandleFunc("PATCH /api/uploads/{id}", s.handlePatchUpload)
	mux.HandleFunc("DELETE /api/uploads/{id}", s.handleDeleteUpload)
	mux.HandleFunc("GET /transfer/content", s.handleContent)
	mux.HandleFunc("HEAD /transfer/content", s.handleContent)
	mux.HandleFunc("GET /selection/archive/{id}", s.handleDownloadSelectionArchive)

	mux.Handle("/", webassets.Handler())

	var handler http.Handler = mux
	handler = s.requireAuthentication(handler)
	handler = requireApplicationRequestHeader(handler)
	handler = s.transferCORS(handler)
	handler = requestLogger(handler)
	handler = recoverPanics(handler)
	handler = s.securityHeaders(handler)
	return handler
}

func (s *Server) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transfer/content" || strings.HasPrefix(r.URL.Path, "/selection/archive/") {
			if !s.auth.authenticated(r) {
				writeError(w, http.StatusUnauthorized, errors.New("请先登录"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/health", "/api/session", "/api/auth/login", "/api/webhooks/stun":
			next.ServeHTTP(w, r)
			return
		}
		if !s.auth.authenticated(r) && !s.authorizeTransferRequest(r) {
			writeError(w, http.StatusUnauthorized, errors.New("请先登录"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireApplicationRequestHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") &&
			r.URL.Path != "/api/webhooks/stun" &&
			r.Method != http.MethodGet &&
			r.Method != http.MethodHead &&
			r.Method != http.MethodOptions &&
			r.Header.Get("X-ClawFiles-Request") != "1" {
			writeError(w, http.StatusForbidden, errors.New("请求来源无效"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectSource := "'self'"
		if s.config.LANTransferOrigin != "" {
			connectSource += " " + s.config.LANTransferOrigin
		}
		if s.transfer.enabled() {
			connectSource += " https://" + s.config.STUNTransferDomain + ":*"
		}
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; frame-src 'self'; connect-src "+connectSource+"; object-src 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=()")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/sw.js" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic handling %s %s: %v\n%s", r.Method, r.URL.Path, recovered, debug.Stack())
				writeError(w, http.StatusInternalServerError, errors.New("服务器发生内部错误"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
