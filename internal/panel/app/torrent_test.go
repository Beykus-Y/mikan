package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// The torrent blocker from the admin's side: its settings, a ban on the user's card and
// in the list of catches, and the ban lifted.
func TestTorrentBlockerAPI(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	users := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock)
	caught, err := users.Create(ctx, domain.CreateInput{Name: "caught", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	free, err := users.Create(ctx, domain.CreateInput{Name: "free", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/torrent"
	var v struct {
		Enabled    bool  `json:"enabled"`
		BanMinutes int64 `json:"ban_minutes"`
		Exempt     []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"exempt"`
	}
	resp, body := h.do(http.MethodGet, path, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.Enabled || v.BanMinutes != 60 || v.Exempt == nil {
		t.Fatalf("off by default, an hour's ban ready: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, path, map[string]any{"ban_minutes": 50000}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a ban past 30 days: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, path, map[string]any{"exempt": []int64{999999}}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "user_not_found") {
		t.Fatalf("an unknown user exempt: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, path, map[string]any{"enabled": true, "exempt": []int64{free.ID, free.ID}}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.Enabled || len(v.Exempt) != 1 || v.Exempt[0].Name != "free" {
		t.Fatalf("on, one exempt: %d %s", resp.StatusCode, body)
	}

	until := h.now.Add(time.Hour).Unix()
	if _, err := h.st.Q.AddTorrentHit(ctx, db.AddTorrentHitParams{UserID: caught.ID, NodeID: sql.NullInt64{Int64: 1, Valid: true}, Ip: "203.0.113.5",
		Inbound: "vless-reality", Network: "tcp", Kind: "handshake", Dest: "198.51.100.1:6881", Hits: 4, At: h.now.Unix(), BannedUntil: until}); err != nil {
		t.Fatal(err)
	}
	userPath := "/" + adminPath + "/api/v1/users/" + strconv.FormatInt(caught.ID, 10)
	var u struct {
		TorrentBan *time.Time `json:"torrent_ban"`
	}
	resp, body = h.do(http.MethodGet, userPath, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &u) != nil || u.TorrentBan == nil || u.TorrentBan.Unix() != until {
		t.Fatalf("the ban on the card: %d %s", resp.StatusCode, body)
	}
	var hits []struct {
		UserName    string     `json:"user_name"`
		Kind        string     `json:"kind"`
		Hits        int        `json:"hits"`
		BannedUntil *time.Time `json:"banned_until"`
		LiftedAt    *time.Time `json:"lifted_at"`
	}
	resp, body = h.do(http.MethodGet, path+"/hits?user_id="+strconv.FormatInt(caught.ID, 10), nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &hits) != nil || len(hits) != 1 || hits[0].UserName != "caught" || hits[0].Hits != 4 || hits[0].BannedUntil == nil {
		t.Fatalf("the catch listed: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do(http.MethodPost, userPath+"/torrent-ban/lift", nil, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"lifted":1`) {
		t.Fatalf("lift: %d %s", resp.StatusCode, body)
	}
	u.TorrentBan = nil
	resp, body = h.do(http.MethodGet, userPath, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &u) != nil || u.TorrentBan != nil {
		t.Fatalf("no ban after the lift: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodGet, path+"/hits", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &hits) != nil || len(hits) != 1 || hits[0].LiftedAt == nil {
		t.Fatalf("the catch stays, marked lifted: %d %s", resp.StatusCode, body)
	}
}
