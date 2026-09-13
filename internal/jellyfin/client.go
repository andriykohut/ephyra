// Package jellyfin is a thin read-only HTTP client for a Jellyfin server: the
// live /Sessions poll, /System/Info for the header, and item/person/user
// images for the art proxy. It is not a source.Source.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
)

// ErrNotFound marks a 404 from Jellyfin -- the art proxy caches this
// distinctly from a real upstream failure so a missing image isn't retried
// on every render.
var ErrNotFound = errors.New("not found")

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
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	return c.getImage(ctx, path, q)
}

// PersonImage streams a person's primary image, looked up by name -- the
// Persons endpoint has no id that traces back to jellyfin.db. The caller
// copies body to the response and closes it.
func (c *Client) PersonImage(ctx context.Context, name string) (io.ReadCloser, string, error) {
	return c.getImage(ctx, "/Persons/"+url.PathEscape(name)+"/Images/Primary", nil)
}

func (c *Client) UserImage(ctx context.Context, userID string) (io.ReadCloser, string, error) {
	return c.getImage(ctx, "/Users/"+url.PathEscape(userID)+"/Images/Primary", nil)
}

func (c *Client) getImage(ctx context.Context, path string, q url.Values) (io.ReadCloser, string, error) {
	if q == nil {
		q = url.Values{}
	}
	// These cards render at a few hundred px at most, so there is no reason to
	// pull the original. Jellyfin resizes server-side and this is the
	// difference between a few KB and a few MB per session.
	q.Set("maxWidth", "480")
	q.Set("quality", "85")
	u := c.base + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "MediaBrowser Token="+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("jellyfin %s: %w", path, ErrNotFound)
	}
	if resp.StatusCode/100 != 2 {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("jellyfin %s: %s", path, resp.Status)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// FindPerson resolves a name to Jellyfin's own person id. searchTerm is a
// substring match, so only an exact name counts -- "Cranston" also returns
// "Cranston Johnson". Empty string means no match, which is not an error.
func (c *Client) FindPerson(ctx context.Context, name string) (string, error) {
	var out struct {
		Items []struct{ Name, Id string }
	}
	q := url.Values{"searchTerm": {name}, "limit": {"20"}}
	if err := c.getJSON(ctx, "/Persons?"+q.Encode(), &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		if it.Name == name {
			return it.Id, nil
		}
	}
	return "", nil
}

func (c *Client) Genres(ctx context.Context) (map[string]string, error) {
	var out struct {
		Items []struct{ Name, Id string }
	}
	if err := c.getJSON(ctx, "/Genres?limit=1000", &out); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(out.Items))
	for _, it := range out.Items {
		m[it.Name] = it.Id
	}
	return m, nil
}

func (c *Client) ServerID(ctx context.Context) (string, error) {
	var out struct{ Id string }
	if err := c.getJSON(ctx, "/System/Info", &out); err != nil {
		return "", err
	}
	return out.Id, nil
}
