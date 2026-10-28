package arr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSlskdVersionShapes(t *testing.T) {
	for body, want := range map[string]string{
		`{"version":"0.21.4.0"}`: "0.21.4.0",
		`{"version":{"full":"0.26.0.0 (0.26.0.0+e42a525d)","current":"0.26.0.0","latest":"0.26.1"}}`: "0.26.0.0",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v0/application" || r.Header.Get("X-API-Key") != "k" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(body))
		}))
		got, err := NewSlskd(srv.URL, "k").Version(context.Background())
		srv.Close()
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", body, got, err, want)
		}
	}
}
