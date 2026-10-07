package panelimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// Marzban pages by offset and limit; every page is read, and the last one ends it.
func TestFetchMarzbanPages(t *testing.T) {
	defer func(n int) { marzbanPage = n }(marzbanPage)
	marzbanPage = 2
	all := []map[string]any{}
	for i := range 5 {
		all = append(all, map[string]any{"username": fmt.Sprintf("u%d", i), "status": "active", "expire": 1893456000, "used_traffic": i})
	}
	var asked []string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok"})
		case "/api/users":
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			asked = append(asked, r.URL.RawQuery)
			end := min(off+lim, len(all))
			_ = json.NewEncoder(w).Encode(map[string]any{"users": all[min(off, end):end], "total": len(all)})
		}
	})
	got, err := Fetch(context.Background(), Client(), Source{Kind: Marzban, URL: srv.URL, Username: "a", Password: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[4].Name != "u4" || got[4].Used != 4 || len(asked) != 3 {
		t.Fatalf("got %d users in %d pages: %v", len(got), len(asked), asked)
	}
}

// PasarGuard with an API key: X-Api-Key, no login; the term is an ISO date with an offset,
// users have ids, and limited and expired users come with their status.
func TestFetchPasarGuardWithAPIKey(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/token" {
			t.Error("logged in although an API key was given")
		}
		if r.Header.Get("X-Api-Key") != "pg_key_1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total": 3, "users": []map[string]any{
			{"id": 42, "username": "olga", "status": "active", "expire": "2030-01-01T00:00:00+03:00", "hwid_limit": 3},
			{"id": 43, "username": "lim", "status": "limited", "data_limit": 100, "used_traffic": 100, "expire": nil},
			{"id": 44, "username": "exp", "status": "expired", "expire": "2025-01-01T00:00:00Z"},
		}})
	})
	got, err := Fetch(context.Background(), Client(), Source{Kind: PasarGuard, URL: srv.URL, Token: "pg_key_1"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2029, 12, 31, 21, 0, 0, 0, time.UTC)
	if len(got) != 3 || got[0].SourceID != 42 || !got[0].Expires.Equal(want) || got[0].DeviceLimit != 3 ||
		got[1].Status != StatusLimited || got[1].TrafficLimit != 100 || got[2].Status != StatusExpired {
		t.Fatalf("got %+v", got)
	}
	if _, err := Fetch(context.Background(), Client(), Source{Kind: PasarGuard, URL: srv.URL, Token: "wrong"}); !errors.Is(err, ErrAuth) {
		t.Fatalf("a wrong key: %v", err)
	}
}

// Remnawave's cursor: pages until hasMore is false; a cursor that keeps moving with empty
// pages stops at the first empty one instead of going on forever.
func TestFetchRemnawaveCursor(t *testing.T) {
	page := func(users []map[string]any, next any, more bool) map[string]any {
		return map[string]any{"response": map[string]any{"users": users, "nextCursor": next, "hasMore": more}}
	}
	user := func(n string) map[string]any {
		return map[string]any{"shortUuid": "Short0Uuid0Long0" + n, "username": n, "status": "ACTIVE", "expireAt": "2099-01-01T00:00:00Z", "userTraffic": map[string]any{"usedTrafficBytes": 1}}
	}
	calls := 0
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("cursor") {
		case "":
			_ = json.NewEncoder(w).Encode(page([]map[string]any{user("a"), user("b")}, "2", true))
		case "2":
			_ = json.NewEncoder(w).Encode(page([]map[string]any{user("c")}, "3", true))
		default: // a broken source: a new cursor every time, no users
			_ = json.NewEncoder(w).Encode(page(nil, strconv.Itoa(calls+10), true))
		}
	})
	got, err := Fetch(context.Background(), Client(), Source{Kind: Remnawave, URL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Token != "Short0Uuid0Long0c" || !got[0].Expires.IsZero() || calls != 3 {
		t.Fatalf("got %d users in %d calls", len(got), calls)
	}
}

// Remnawave before 2.8 has no stream: the paged list is read instead.
func TestFetchRemnawaveOldList(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/users/stream" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"total": 1, "users": []map[string]any{
			{"shortUuid": "Old1", "username": "old", "status": "DISABLED", "expireAt": "2030-01-01T00:00:00Z"},
		}}})
	})
	got, err := Fetch(context.Background(), Client(), Source{Kind: Remnawave, URL: srv.URL, Token: "t"})
	if err != nil || len(got) != 1 || got[0].Status != StatusDisabled {
		t.Fatalf("got %+v %v", got, err)
	}
}

// A redirect is not followed: a 307 on the login would carry the password elsewhere.
func TestFetchFollowsNoRedirect(t *testing.T) {
	elsewhere := serve(t, func(w http.ResponseWriter, r *http.Request) { t.Error("the request went to the other host") })
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	_, err := Fetch(context.Background(), Client(), Source{Kind: Marzban, URL: srv.URL, Username: "a", Password: "secret"})
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("a redirect: %v", err)
	}
}

// Plain http only to this server or the LAN; the metadata address never, by any scheme.
func TestFetchAddresses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for url, want := range map[string]error{
		"http://203.0.113.10":           ErrAddress, // a public address over plain http
		"http://169.254.169.254":        ErrAddress, // cloud metadata
		"https://169.254.169.254":       ErrAddress,
		"https://[fe80::1]:443":         ErrAddress,
		"http://0.0.0.0:80":             ErrAddress,
		"ftp://panel.example.com":       ErrAnswer,
		"https://user:pw@panel.example": ErrAnswer,
	} {
		_, err := Fetch(ctx, Client(), Source{Kind: Marzban, URL: url, Username: "a", Password: "b"})
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", url, err, want)
		}
	}
	for _, ok := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.2", "203.0.113.10"} {
		if !AddressOK(mustAddr(ok)) {
			t.Errorf("%s is refused", ok)
		}
	}
}

// A certificate nobody trusts has its own code, and the import does not skip the check.
func TestFetchUntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := Fetch(context.Background(), Client(), Source{Kind: Marzban, URL: srv.URL, Username: "a", Password: "b"})
	if !errors.Is(err, ErrTLS) || Code(err) != "import_tls" {
		t.Fatalf("an untrusted certificate: %v", err)
	}
}

// What the old panel sends is checked against mikan's limits.
func TestNormalize(t *testing.T) {
	ok := User{Name: " ivan ", Used: -5, Lifetime: -1, DeviceLimit: 500, Expires: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	u, err := Normalize(ok)
	if err != nil || u.Name != "ivan" || u.Used != 0 || u.Lifetime != 0 || u.DeviceLimit != maxDevices {
		t.Fatalf("normalized %+v %v", u, err)
	}
	for name, bad := range map[string]User{
		"no name":      {Name: "  "},
		"long name":    {Name: strings.Repeat("я", 101)},
		"control char": {Name: "a\x00b"},
		"long contact": {Name: "a", Contact: strings.Repeat("x", 101)},
		"long note":    {Name: "a", Note: strings.Repeat("x", 2001)},
		"neg limit":    {Name: "a", TrafficLimit: -1},
		"huge expire":  {Name: "a", Expires: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)},
		"old expire":   {Name: "a", Expires: time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC)},
		"huge on hold": {Name: "a", OnHold: 100 * 365 * 24 * time.Hour},
	} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Numbers past what time.Unix and time.Duration hold do not wrap into a valid term.
func TestHostileNumbers(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok"})
		default:
			_, _ = w.Write([]byte(`{"total":2,"users":[{"username":"a","status":"active","expire":1e30,"on_hold_expire_duration":9223372036854775807},{"username":"` + strings.Repeat("b", 100000) + `","status":"active"}]}`))
		}
	})
	got, err := Fetch(context.Background(), Client(), Source{Kind: Marzban, URL: srv.URL, Username: "a", Password: "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range got {
		if _, err := Normalize(u); err == nil {
			t.Errorf("%.20s… accepted: %+v", u.Name, u.Expires)
		}
	}
	if len(got[1].Name) > maxNameBytes {
		t.Errorf("a %d-byte name is kept in memory", len(got[1].Name))
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
