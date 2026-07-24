package webassets

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerServesIndexWithoutRedirect(t *testing.T) {
	handler := Handler()
	for _, requestPath := range []string{"/", "/index.html", "/files/example"} {
		t.Run(requestPath, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, requestPath, nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("GET %s returned %d, want %d", requestPath, response.Code, http.StatusOK)
			}
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("GET %s redirected to %q", requestPath, location)
			}
		})
	}
}
