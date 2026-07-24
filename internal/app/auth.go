package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "clawfiles_session"
	sessionLifetime   = 30 * 24 * time.Hour
)

type loginAttempt struct {
	windowStart time.Time
	count       int
}

type authenticator struct {
	password     string
	cookieSecure bool
	key          []byte
	mu           sync.Mutex
	attempts     map[string]loginAttempt
}

func newAuthenticator(password string, cookieSecure bool) *authenticator {
	sum := sha256.Sum256([]byte("clawfiles/session/" + password))
	return &authenticator{
		password:     password,
		cookieSecure: cookieSecure,
		key:          sum[:],
		attempts:     make(map[string]loginAttempt),
	}
}

func (a *authenticator) enabled() bool {
	return a.password != ""
}

func (a *authenticator) authenticated(r *http.Request) bool {
	if !a.enabled() {
		return true
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expiryText, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expiryUnix, err := strconv.ParseInt(string(expiryText), 10, 64)
	if err != nil || time.Now().Unix() >= expiryUnix {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	expected := a.sign(parts[0])
	return hmac.Equal(signature, expected)
}

func (a *authenticator) login(w http.ResponseWriter, r *http.Request) {
	if !a.allowAttempt(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, errors.New("登录尝试过多，请稍后再试"))
		return
	}

	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("登录请求格式错误"))
		return
	}
	if !a.enabled() || subtle.ConstantTimeCompare([]byte(request.Password), []byte(a.password)) != 1 {
		writeError(w, http.StatusUnauthorized, errors.New("密码错误"))
		return
	}

	expiry := time.Now().Add(sessionLifetime)
	expiryPart := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(expiry.Unix(), 10)))
	signaturePart := base64.RawURLEncoding.EncodeToString(a.sign(expiryPart))
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    expiryPart + "." + signaturePart,
		Path:     "/",
		Expires:  expiry,
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *authenticator) logout(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *authenticator) sign(value string) []byte {
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (a *authenticator) allowAttempt(ip string) bool {
	const window = 5 * time.Minute
	const maxAttempts = 10

	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.attempts[ip]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) > window {
		entry = loginAttempt{windowStart: now}
	}
	entry.count++
	a.attempts[ip] = entry
	return entry.count <= maxAttempts
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
