// Package jellyfin is a thin read-only HTTP client for a Jellyfin server: the
// live /Sessions poll, /System/Info for the header, and item images for the
// Now Playing art proxy. It is not a source.Source.
package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
)

type Client struct {
	base string
	key  string
	hc   *http.Client
}

func New(cfg config.Config) *Client {
	return &Client{
		base: strings.TrimRight(cfg.JellyfinURL, "/"),
		key:  cfg.JellyfinAPIKey,
		hc:   &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "MediaBrowser Token="+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("jellyfin %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Sessions(ctx context.Context) ([]RawSession, error) {
	var ss []RawSession
	err := c.getJSON(ctx, "/Sessions?ActiveWithinSeconds=960", &ss)
	return ss, err
}

func (c *Client) SystemInfo(ctx context.Context) (ServerInfo, error) {
	var si ServerInfo
	err := c.getJSON(ctx, "/System/Info", &si)
	return si, err
}

// Image streams an item image. The caller copies body to the response and
// closes it.
func (c *Client) Image(ctx context.Context, itemID string, kind ImageKind, tag string) (io.ReadCloser, string, error) {
	path := "/Items/" + url.PathEscape(itemID) + "/Images/" + string(kind)
	u := c.base + path
	if tag != "" {
		u += "?tag=" + url.QueryEscape(tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "MediaBrowser Token="+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("jellyfin %s: %s", path, resp.Status)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}
