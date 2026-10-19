package app

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmptyDeleteHeaders(t *testing.T) {
	a := &App{Config: Config{PublicURL: "http://localhost:3000"}}
	for _, tc := range []struct {
		name, method, origin, body string
		want                       int
	}{
		{"empty delete reaches router", "DELETE", a.Config.PublicURL, "", 404},
		{"delete still checks origin", "DELETE", "https://other.example", "", 403},
		{"delete with body requires JSON", "DELETE", a.Config.PublicURL, "text", 415},
		{"post still requires JSON", "POST", a.Config.PublicURL, "{}", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/missing", strings.NewReader(tc.body))
			req.Header.Set("Origin", tc.origin)
			response := httptest.NewRecorder()
			a.Handler().ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("want %d got %d", tc.want, response.Code)
			}
		})
	}
}
