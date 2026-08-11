package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/store"
)

// bridgeTokenHeader carries the shared secret on every request to clark.
const bridgeTokenHeader = "X-Clark-Bridge-Token"

// Client talks to the clark bridge server over HTTPS.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds an HTTPS client. rootCA, when non-empty, points at a PEM
// file whose CA is trusted in addition to the system pool (mkcert fallback).
// baseURL must include the scheme, e.g. "https://clark.example.com".
func NewClient(baseURL, token, rootCA string) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if rootCA != "" {
		pem, err := os.ReadFile(rootCA)
		if err != nil {
			return nil, fmt.Errorf("fail to read root CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from root CA file")
		}
		tlsCfg.RootCAs = pool
	}

	return &Client{
		baseURL: baseURL,
		token:   token,
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// PostInbound forwards one inbound message to clark.
func (c *Client) PostInbound(ctx context.Context, msg imessage.InboundMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("fail to marshal inbound message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inbound", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("inbound rejected with status %d", resp.StatusCode)
	}
	return nil
}

// NextOutbound claims the next pending outbound message. ok is false when the
// queue is empty.
func (c *Client) NextOutbound(ctx context.Context) (store.OutboundMessage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/outbound", nil)
	if err != nil {
		return store.OutboundMessage{}, false, err
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return store.OutboundMessage{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return store.OutboundMessage{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return store.OutboundMessage{}, false, fmt.Errorf("outbound request failed with status %d", resp.StatusCode)
	}

	var msg store.OutboundMessage
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return store.OutboundMessage{}, false, fmt.Errorf("fail to decode outbound message: %w", err)
	}
	return msg, true, nil
}

// Ack confirms delivery of one outbound message.
func (c *Client) Ack(ctx context.Context, id int64) error {
	body, err := json.Marshal(imessage.AckRequest{ID: id})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/ack", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ack rejected with status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set(bridgeTokenHeader, c.token)
	}
}
