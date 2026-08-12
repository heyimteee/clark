package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestSPAServesConsole(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/web/")
	if err != nil {
		t.Fatalf("get /web/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /web/ = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
}

func TestStaticAssetsServed(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	for _, asset := range []struct {
		path, wantCT string
	}{
		{"/web/static/app.css", "text/css"},
		{"/web/static/app.js", "text/javascript"},
	} {
		resp, err := http.Get(ts.URL + asset.path)
		if err != nil {
			t.Fatalf("get %s: %v", asset.path, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("GET %s = %d, want 200", asset.path, resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, asset.wantCT) {
			resp.Body.Close()
			t.Fatalf("GET %s Content-Type = %q, want %q", asset.path, ct, asset.wantCT)
		}
		resp.Body.Close()
	}
}

func TestSPAAssetsPresentInIndex(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/web/")
	if err != nil {
		t.Fatalf("get /web/: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	html := string(buf[:n])

	for _, want := range []string{"app.css", "app.js", "id=\"app\"", "clark"} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
}
