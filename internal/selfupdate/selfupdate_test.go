package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoos/dejima/internal/version"
)

func TestDetectMode(t *testing.T) {
	orig := version.Version
	defer func() { version.Version = orig }()

	version.Version = "v0.1.9"
	if m := DetectMode(); m != ModeRelease {
		t.Errorf("release version → %q, want release", m)
	}
	version.Version = "dev"
	if m := DetectMode(); m != ModeSource {
		t.Errorf("dev version → %q, want source", m)
	}
	version.Version = "v0.1.9-6-gd39c034" // git-describe → still a checkout
	if m := DetectMode(); m != ModeSource {
		t.Errorf("describe version → %q, want source", m)
	}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.8", "v0.1.9", true},  // newer available
		{"v0.1.9", "v0.1.9", false}, // current
		{"v0.2.0", "v0.1.9", false}, // ahead (local newer)
		{"dev", "v0.1.9", true},     // dev sorts below any release → update available
	}
	for _, c := range cases {
		got := Evaluate(c.current, c.latest, ModeRelease)
		if got.UpdateAvailable != c.want {
			t.Errorf("Evaluate(%s,%s).UpdateAvailable = %v, want %v", c.current, c.latest, got.UpdateAvailable, c.want)
		}
	}
}

func TestLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.2.3","name":"whatever"}`))
	}))
	defer srv.Close()
	orig := releasesURL
	releasesURL = srv.URL
	defer func() { releasesURL = orig }()

	tag, err := LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("tag = %q, want v1.2.3", tag)
	}
}

func TestLatestReleaseHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := releasesURL
	releasesURL = srv.URL
	defer func() { releasesURL = orig }()

	if _, err := LatestRelease(context.Background()); err == nil {
		t.Fatal("expected error on HTTP 404")
	}
}
