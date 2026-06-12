package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientBearerHeader verifies the in-island autonomy client attaches its
// token, and that a plain TCP client (tailnet path) sends no Authorization.
func TestClientBearerHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Run("with token", func(t *testing.T) {
		gotAuth = ""
		c, err := NewTCPClientWithToken(srv.URL, "secret-token")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer secret-token")
		}
	})

	t.Run("without token", func(t *testing.T) {
		gotAuth = "sentinel"
		c, err := NewTCPClient(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "" {
			t.Fatalf("Authorization = %q, want empty (no token attached)", gotAuth)
		}
	})
}
