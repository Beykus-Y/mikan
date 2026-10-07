package panelimport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Remnawave 3.x: GET /api/users/stream?size=&cursor= with an API token, every answer in
// {"response": …}. Traffic is in userTraffic, "never" is a term in the year 2099, and the
// backend drops a connection without the headers its reverse proxy adds.

// remnawavePage is the page size; a variable for the tests.
var remnawavePage = 500

type remnawaveUser struct {
	ID                int64   `json:"id"`
	ShortUUID         string  `json:"shortUuid"`
	Username          string  `json:"username"`
	Status            string  `json:"status"`
	TrafficLimitBytes int64   `json:"trafficLimitBytes"`
	ExpireAt          string  `json:"expireAt"`
	Description       *string `json:"description"`
	TelegramID        *int64  `json:"telegramId"`
	Email             *string `json:"email"`
	HWIDDeviceLimit   *int64  `json:"hwidDeviceLimit"`
	UserTraffic       struct {
		UsedTrafficBytes         int64 `json:"usedTrafficBytes"`
		LifetimeUsedTrafficBytes int64 `json:"lifetimeUsedTrafficBytes"`
	} `json:"userTraffic"`
}

func fetchRemnawave(ctx context.Context, hc *http.Client, src Source) ([]User, error) {
	if src.Token == "" {
		return nil, ErrAuth
	}
	var out []User
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("%w: more than %d pages", ErrAnswer, maxPages)
		}
		q := url.Values{"size": {strconv.Itoa(remnawavePage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := http.NewRequest(http.MethodGet, src.URL+"/api/users/stream?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+src.Token)
		// A reverse proxy in front overwrites these; straight to the backend they are needed.
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		var page struct {
			Response struct {
				Users      []remnawaveUser `json:"users"`
				NextCursor *string         `json:"nextCursor"`
				HasMore    bool            `json:"hasMore"`
			} `json:"response"`
		}
		if err := getJSON(ctx, hc, req, &page); errors.Is(err, errNotFound) && cursor == "" {
			// Before 2.8.0 Remnawave has no stream: the paged list.
			return fetchRemnawavePaged(ctx, hc, src)
		} else if err != nil {
			return nil, err
		}
		for _, ru := range page.Response.Users {
			u, err := ru.user()
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		if len(out) > maxUsers {
			return nil, fmt.Errorf("%w: more than %d users", ErrAnswer, maxUsers)
		}
		// An empty page ends it too: a cursor that moves without users would go on forever.
		if !page.Response.HasMore || len(page.Response.Users) == 0 || page.Response.NextCursor == nil || *page.Response.NextCursor == "" || *page.Response.NextCursor == cursor {
			return out, nil
		}
		cursor = *page.Response.NextCursor
	}
}

// strongToken is what a Remnawave short UUID has to look like to stand as a link's secret.
var strongToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// fetchRemnawavePaged reads GET /api/users?start=&size=, which Remnawave 2.x has (the
// same user objects, with userTraffic).
func fetchRemnawavePaged(ctx context.Context, hc *http.Client, src Source) ([]User, error) {
	var out []User
	for page, start := 0, 0; ; page, start = page+1, start+remnawavePage {
		if page >= maxPages {
			return nil, fmt.Errorf("%w: more than %d pages", ErrAnswer, maxPages)
		}
		q := url.Values{"start": {strconv.Itoa(start)}, "size": {strconv.Itoa(remnawavePage)}}
		req, err := http.NewRequest(http.MethodGet, src.URL+"/api/users?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+src.Token)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		var resp struct {
			Response struct {
				Users []remnawaveUser `json:"users"`
				Total int             `json:"total"`
			} `json:"response"`
		}
		if err := getJSON(ctx, hc, req, &resp); err != nil {
			return nil, err
		}
		for _, ru := range resp.Response.Users {
			u, err := ru.user()
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		if len(out) > maxUsers {
			return nil, fmt.Errorf("%w: more than %d users", ErrAnswer, maxUsers)
		}
		if len(resp.Response.Users) < remnawavePage || len(out) >= resp.Response.Total {
			return out, nil
		}
	}
}

func (r remnawaveUser) user() (User, error) {
	u := User{Name: cut(strings.TrimSpace(r.Username), maxNameBytes), Token: cut(strings.TrimSpace(r.ShortUUID), 128),
		Used: r.UserTraffic.UsedTrafficBytes, Lifetime: r.UserTraffic.LifetimeUsedTrafficBytes}
	if u.Name == "" {
		return u, fmt.Errorf("%w: a user without a username", ErrAnswer)
	}
	// The short UUID is the whole secret of a Remnawave link: one too short or odd to be
	// random is not taken over (the user comes without the old link).
	if !strongToken.MatchString(u.Token) {
		u.Token, u.WeakToken = "", true
	}
	switch r.Status {
	case "DISABLED":
		u.Status = StatusDisabled
	case "LIMITED":
		u.Status = StatusLimited
	case "EXPIRED":
		u.Status = StatusExpired
	default:
		u.Status = StatusActive
	}
	if r.TrafficLimitBytes > 0 {
		u.TrafficLimit = r.TrafficLimitBytes
	}
	if u.Lifetime < u.Used {
		u.Lifetime = u.Used
	}
	if r.ExpireAt != "" {
		t, err := time.Parse(time.RFC3339Nano, r.ExpireAt)
		if err != nil {
			return u, fmt.Errorf("%w: user %s: expireAt %q", ErrAnswer, u.Name, r.ExpireAt)
		}
		if t.Year() < 2099 { // Remnawave's "never"
			u.Expires = t.UTC()
		}
	}
	if r.Description != nil {
		u.Note = cut(*r.Description, maxNoteBytes)
	}
	switch {
	case r.TelegramID != nil && *r.TelegramID != 0:
		u.Contact = "tg:" + strconv.FormatInt(*r.TelegramID, 10)
	case r.Email != nil:
		u.Contact = cut(*r.Email, maxNameBytes)
	}
	if r.HWIDDeviceLimit != nil && *r.HWIDDeviceLimit > 0 {
		u.DeviceLimit = *r.HWIDDeviceLimit
	}
	return u, nil
}
