// Package crowdin is a small read-only client of Crowdin's REST API v2: just
// what pulling a project into bellingua needs (project, files, strings,
// translations, approvals). It never writes to Crowdin; the token needs only
// read scopes (see README).
package crowdin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBase is crowdin.com; Crowdin Enterprise uses
// https://{organization}.api.crowdin.com/api/v2.
const DefaultBase = "https://api.crowdin.com/api/v2"

const pageSize = 500

// Client talks to one Crowdin API.
type Client struct {
	Base  string // API root including /api/v2; DefaultBase when empty
	Token string // personal access token (read-only scopes are enough)
	HTTP  *http.Client
	// Sleep waits out a 429; tests replace it.
	Sleep func(context.Context, time.Duration) error
}

func (c *Client) base() string {
	if c.Base == "" {
		return DefaultBase
	}
	return strings.TrimRight(c.Base, "/")
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Error is a non-2xx answer.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "crowdin: the token was refused (401); check it and that it is for this organization"
	case http.StatusForbidden:
		return "crowdin: the token lacks a scope or access to this project (403): " + e.Message
	case http.StatusNotFound:
		return "crowdin: not found (404): " + e.Message
	}
	return fmt.Sprintf("crowdin: HTTP %d: %s", e.Status, e.Message)
}

// get fetches one URL, retrying a rate-limited (429) call a few times.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("crowdin: no token (set crowdin.token in the config or $%s)", TokenEnv)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	wait := c.Sleep
	if wait == nil {
		wait = sleep
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("crowdin: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("crowdin: %w", err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
			d := 2 * time.Second << attempt
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
				d = time.Duration(s) * time.Second
			}
			if err := wait(ctx, d); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode/100 != 2 {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			msg := strings.TrimSpace(string(body))
			if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
				msg = e.Error.Message
			}
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return nil, &Error{Status: resp.StatusCode, Message: msg}
		}
		return body, nil
	}
}

// list walks every page of a collection, calling fn on each item's "data".
func (c *Client) list(ctx context.Context, path string, q url.Values, fn func(json.RawMessage) error) error {
	for offset := 0; ; offset += pageSize {
		v := url.Values{}
		for k, s := range q {
			v[k] = s
		}
		v.Set("limit", strconv.Itoa(pageSize))
		v.Set("offset", strconv.Itoa(offset))
		body, err := c.get(ctx, c.base()+path+"?"+v.Encode())
		if err != nil {
			return err
		}
		var page struct {
			Data []struct {
				Data json.RawMessage `json:"data"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("crowdin: %s: %w", path, err)
		}
		for _, it := range page.Data {
			if err := fn(it.Data); err != nil {
				return err
			}
		}
		if len(page.Data) < pageSize {
			return nil
		}
	}
}

// TokenEnv is the environment variable read when the config has no token.
const TokenEnv = "BELLINGUA_CROWDIN_TOKEN"

// Project is the part of a Crowdin project bellingua keeps.
type Project struct {
	ID          int64
	Name        string
	Identifier  string // the slug in editor URLs
	SourceLang  string
	TargetLangs []string
}

// GetProject reads one project.
func (c *Client) GetProject(ctx context.Context, id int64) (Project, error) {
	body, err := c.get(ctx, fmt.Sprintf("%s/projects/%d", c.base(), id))
	if err != nil {
		return Project{}, err
	}
	var r struct {
		Data struct {
			ID               int64    `json:"id"`
			Name             string   `json:"name"`
			Identifier       string   `json:"identifier"`
			SourceLanguageID string   `json:"sourceLanguageId"`
			TargetLanguageID []string `json:"targetLanguageIds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Project{}, fmt.Errorf("crowdin: project: %w", err)
	}
	d := r.Data
	return Project{ID: id, Name: d.Name, Identifier: d.Identifier, SourceLang: d.SourceLanguageID, TargetLangs: d.TargetLanguageID}, nil
}

// File is a source file; Path has no leading slash.
type File struct {
	ID   int64
	Path string
}

// Files lists every source file of the project.
func (c *Client) Files(ctx context.Context, project int64) ([]File, error) {
	var out []File
	err := c.list(ctx, fmt.Sprintf("/projects/%d/files", project), nil, func(raw json.RawMessage) error {
		var f struct {
			ID   int64  `json:"id"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		out = append(out, File{ID: f.ID, Path: strings.TrimPrefix(f.Path, "/")})
		return nil
	})
	return out, err
}

// String is a source string. Plural strings have no Text.
type String struct {
	ID         int64
	FileID     int64
	Identifier string
	Text       string
	Context    string
	Plural     bool
}

// plainText returns the string a "text" field holds, or false for a plural
// object or null.
func plainText(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return "", false
}

// Strings lists every source string of the project.
func (c *Client) Strings(ctx context.Context, project int64) ([]String, error) {
	var out []String
	err := c.list(ctx, fmt.Sprintf("/projects/%d/strings", project), nil, func(raw json.RawMessage) error {
		var s struct {
			ID         int64           `json:"id"`
			FileID     int64           `json:"fileId"`
			Identifier string          `json:"identifier"`
			Text       json.RawMessage `json:"text"`
			Context    string          `json:"context"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		text, ok := plainText(s.Text)
		out = append(out, String{ID: s.ID, FileID: s.FileID, Identifier: s.Identifier, Text: text, Context: s.Context, Plural: !ok})
		return nil
	})
	return out, err
}

// Translations maps string id to the current translation into lang. Plural
// translations are left out.
func (c *Client) Translations(ctx context.Context, project int64, lang string) (map[int64]string, error) {
	out := map[int64]string{}
	err := c.list(ctx, fmt.Sprintf("/projects/%d/languages/%s/translations", project, url.PathEscape(lang)), nil, func(raw json.RawMessage) error {
		var t struct {
			StringID int64           `json:"stringId"`
			Text     json.RawMessage `json:"text"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		if text, ok := plainText(t.Text); ok {
			out[t.StringID] = text
		}
		return nil
	})
	return out, err
}

// Approved is the set of string ids that have an approved translation into lang.
func (c *Client) Approved(ctx context.Context, project int64, lang string) (map[int64]bool, error) {
	out := map[int64]bool{}
	err := c.list(ctx, fmt.Sprintf("/projects/%d/approvals", project), url.Values{"languageId": {lang}}, func(raw json.RawMessage) error {
		var a struct {
			StringID int64 `json:"stringId"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out[a.StringID] = true
		return nil
	})
	return out, err
}

var orgRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// BaseForOrg turns a Crowdin Enterprise organization into its API root ("" is
// crowdin.com). The host is never taken from user input, so a token can only
// be sent to crowdin.com or to {organization}.api.crowdin.com.
func BaseForOrg(org string) (string, error) {
	org = strings.ToLower(strings.TrimSpace(org))
	if org == "" {
		return "", nil
	}
	if !orgRe.MatchString(org) {
		return "", fmt.Errorf("organization %q is not a valid Crowdin Enterprise domain name", org)
	}
	return "https://" + org + ".api.crowdin.com/api/v2", nil
}
