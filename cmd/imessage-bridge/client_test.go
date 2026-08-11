package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/store"
)

func TestNewClientRootCA(t *testing.T) {
	_, err := NewClient("https://example.com", "tok", filepath.Join(t.TempDir(), "missing.pem"))
	if err == nil {
		t.Fatal("missing root CA file should error")
	}
}

// testRootCA writes the httptest server's self-signed cert to a temp PEM file,
// exercising the same IMESSAGE_TLS_ROOTCA trust path the production bridge uses.
func testRootCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	path := filepath.Join(t.TempDir(), "root.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write root CA: %v", err)
	}
	return path
}

func TestClientHTTPFlow(t *testing.T) {
	var gotToken string
	var gotInbound imessage.InboundMessage
	var acked []int64
	outbound := store.OutboundMessage{ID: 99, Recipient: "+6281267858909", Text: "hello"}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(bridgeTokenHeader)
		switch r.URL.Path {
		case "/inbound":
			json.NewDecoder(r.Body).Decode(&gotInbound)
			w.WriteHeader(http.StatusOK)
		case "/outbound":
			if gotToken != "" {
				json.NewEncoder(w).Encode(outbound)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case "/ack":
			var req imessage.AckRequest
			json.NewDecoder(r.Body).Decode(&req)
			acked = append(acked, req.ID)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "secret", testRootCA(t, srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	msg := imessage.InboundMessage{ID: "5", Handle: "+6281267858909", Text: "hi", IsSelf: false}
	if err := client.PostInbound(ctx, msg); err != nil {
		t.Fatalf("PostInbound: %v", err)
	}
	if gotToken != "secret" {
		t.Errorf("token = %q, want secret", gotToken)
	}
	if gotInbound.ID != "5" || gotInbound.Handle != msg.Handle || gotInbound.Text != msg.Text {
		t.Errorf("inbound = %+v, want %+v", gotInbound, msg)
	}

	got, ok, err := client.NextOutbound(ctx)
	if err != nil || !ok {
		t.Fatalf("NextOutbound = ok:%v err:%v", ok, err)
	}
	if got.ID != outbound.ID || got.Recipient != outbound.Recipient || got.Text != outbound.Text {
		t.Errorf("outbound = %+v, want %+v", got, outbound)
	}

	if err := client.Ack(ctx, 99); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if len(acked) != 1 || acked[0] != 99 {
		t.Errorf("acked = %v, want [99]", acked)
	}
}

func TestClientEmptyOutbound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "", testRootCA(t, srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, ok, err := client.NextOutbound(context.Background())
	if err != nil {
		t.Fatalf("NextOutbound: %v", err)
	}
	if ok {
		t.Fatal("empty queue should report ok=false")
	}
}

func TestClientErrorPropagation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "", testRootCA(t, srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	if err := client.PostInbound(ctx, imessage.InboundMessage{ID: "1"}); err == nil {
		t.Error("403 inbound should error")
	}
	if _, _, err := client.NextOutbound(ctx); err == nil {
		t.Error("403 outbound should error")
	}
	if err := client.Ack(ctx, 1); err == nil {
		t.Error("403 ack should error")
	}
}
