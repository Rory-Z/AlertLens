package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	var readyErr error
	h := New(func() error { return readyErr })

	assertResponse := func(path string, wantStatus int, wantBody string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != wantStatus || w.Body.String() != wantBody {
			t.Fatalf("%s response = (%d, %q), want (%d, %q)", path, w.Code, w.Body.String(), wantStatus, wantBody)
		}
	}

	assertResponse("/healthz", http.StatusOK, "ok\n")
	assertResponse("/readyz", http.StatusOK, "ok\n")
	readyErr = errors.New("sensitive state path")
	assertResponse("/readyz", http.StatusServiceUnavailable, "not ready\n")
	assertResponse("/unknown", http.StatusNotFound, "404 page not found\n")
}
