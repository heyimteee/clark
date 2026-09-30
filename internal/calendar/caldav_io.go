package calendar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// newPutRequest builds an authenticated PUT carrying an iCalendar object.
func (c *CalDAVClient) newPutRequest(ctx context.Context, href, ics string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, href, strings.NewReader(ics))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	c.authorize(req)
	return req, nil
}

// put writes an iCalendar object back to its href.
func (c *CalDAVClient) put(ctx context.Context, href, ics string) error {
	req, err := c.newPutRequest(ctx, href, ics)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calendar update failed: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("calendar update returned %s", resp.Status)
	}
	return nil
}

// get fetches a single iCalendar object.
func (c *CalDAVClient) get(ctx context.Context, href string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calendar read failed: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("event not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calendar read returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// authorize applies HTTP Basic credentials. CalDAV has no bearer-token flow, so
// this is the app-specific password over TLS.
func (c *CalDAVClient) authorize(req *http.Request) {
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
}
