package panelimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Marzban and PasarGuard share the API: POST /api/admin/token (a form) for a bearer token,
// GET /api/users?offset=&limit= for {users, total}. PasarGuard also takes an API key in
// X-Api-Key, and gives the term as an ISO date where Marzban gives unix seconds.

// marzbanPage is the page size; a variable for the tests.
var marzbanPage = 500

type marzbanUser struct {
	ID                   int64           `json:"id"` // PasarGuard only
	Username             string          `json:"username"`
	Status               string          `json:"status"`
	DataLimit            *int64          `json:"data_limit"`
	UsedTraffic          int64           `json:"used_traffic"`
	LifetimeUsedTraffic  int64           `json:"lifetime_used_traffic"`
	Expire               json.RawMessage `json:"expire"`
	OnHoldExpireDuration *int64          `json:"on_hold_expire_duration"`
	Note                 *string         `json:"note"`
	CreatedAt            string          `json:"created_at"`
	HWIDLimit            *int64          `json:"hwid_limit"` // PasarGuard only
}

func fetchMarzban(ctx context.Context, hc *http.Client, src Source) ([]User, error) {
	auth := func(r *http.Request) {}
	if src.Kind == PasarGuard && src.Token != "" {
		key := src.Token
		auth = func(r *http.Request) { r.Header.Set("X-Api-Key", key) }
	} else {
		token, err := marzbanToken(ctx, hc, src)
		if err != nil {
			return nil, err
		}
		auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}
	var out []User
	total := -1
	// The offset moves by what a page held: a panel that caps limit below ours gives short
	// pages, and a short page is not the end.
	for n, offset := 0, 0; ; n++ {
		if n >= maxPages {
			return nil, fmt.Errorf("%w: more than %d pages", ErrAnswer, maxPages)
		}
		q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(marzbanPage)}, "sort": {"created_at"}}
		req, err := http.NewRequest(http.MethodGet, src.URL+"/api/users?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		auth(req)
		var page struct {
			Users []marzbanUser `json:"users"`
			Total int           `json:"total"`
		}
		if err := getJSON(ctx, hc, req, &page); err != nil {
			return nil, err
		}
		for _, mu := range page.Users {
			u, err := mu.user()
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		if len(out) > maxUsers {
			return nil, fmt.Errorf("%w: more than %d users", ErrAnswer, maxUsers)
		}
		if total < 0 {
			total = page.Total
		}
		offset += len(page.Users)
		if len(page.Users) == 0 || len(out) >= total {
			break
		}
	}
	// Users missing from what the panel said it has: an import must not report success
	// with a part of them.
	if len(out) < total {
		return nil, fmt.Errorf("%w: the panel listed %d of the %d users it has", ErrIncomplete, len(out), total)
	}
	return out, nil
}

func marzbanToken(ctx context.Context, hc *http.Client, src Source) (string, error) {
	form := url.Values{"username": {src.Username}, "password": {src.Password}}
	req, err := http.NewRequest(http.MethodPost, src.URL+"/api/admin/token", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := getJSON(ctx, hc, req, &t); err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", ErrAuth
	}
	return t.AccessToken, nil
}

func (m marzbanUser) user() (User, error) {
	u := User{Name: cut(strings.TrimSpace(m.Username), maxNameBytes), SourceID: m.ID, Used: m.UsedTraffic, Lifetime: m.LifetimeUsedTraffic}
	if u.Name == "" {
		return u, fmt.Errorf("%w: a user without a username", ErrAnswer)
	}
	switch m.Status {
	case "active", "disabled", "limited", "expired", "on_hold":
		u.Status = m.Status
	default:
		u.Status = StatusActive
	}
	if m.DataLimit != nil && *m.DataLimit > 0 {
		u.TrafficLimit = *m.DataLimit
	}
	if u.Lifetime < u.Used {
		u.Lifetime = u.Used
	}
	exp, err := parseExpire(m.Expire)
	if err != nil {
		return u, fmt.Errorf("%w: user %s: %v", ErrAnswer, u.Name, err)
	}
	u.Expires = exp
	if m.OnHoldExpireDuration != nil && *m.OnHoldExpireDuration > 0 {
		// Checked before the multiplication, which would wrap a huge number into a small one.
		if *m.OnHoldExpireDuration > int64(maxTerm/time.Second) {
			u.OnHold = -1 // out of range: Normalize refuses it
		} else {
			u.OnHold = time.Duration(*m.OnHoldExpireDuration) * time.Second
		}
	}
	if m.CreatedAt != "" {
		if t, err := parseExpire(json.RawMessage(strconv.Quote(m.CreatedAt))); err == nil {
			u.Created = t
		}
	}
	if m.Note != nil {
		u.Note = cut(*m.Note, maxNoteBytes)
	}
	if m.HWIDLimit != nil && *m.HWIDLimit > 0 {
		u.DeviceLimit = *m.HWIDLimit
	}
	return u, nil
}

// parseExpire reads Marzban's unix seconds or PasarGuard's ISO date; null or 0: never.
func parseExpire(raw json.RawMessage) (time.Time, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "0" {
		return time.Time{}, nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return time.Time{}, err
		}
		if str == "" {
			return time.Time{}, nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
			if t, err := time.Parse(layout, str); err == nil {
				return t.UTC(), nil // a date without a zone is UTC, as both panels write it
			}
		}
		return time.Time{}, fmt.Errorf("expire %q", str)
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("expire %s", s)
	}
	if n <= 0 {
		return time.Time{}, nil
	}
	return time.Unix(int64(n), 0).UTC(), nil
}
