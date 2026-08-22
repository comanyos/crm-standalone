package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHostedUIHandler(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"assets/app-abc.css": {Data: []byte("body{}")},
	}
	s := &Server{}
	h := s.hostedUIHandler(dist)

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantBody string
	}{
		{"root serves index", "/", http.StatusOK, "<!doctype html><div id=root></div>"},
		{"asset served directly", "/assets/app-abc.js", http.StatusOK, "console.log(1)"},
		{"unknown client route falls back to index", "/login", http.StatusOK, "<!doctype html><div id=root></div>"},
		{"nested client route falls back to index", "/recovery", http.StatusOK, "<!doctype html><div id=root></div>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := rec.Body.String(); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}
