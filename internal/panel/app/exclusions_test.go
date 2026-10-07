package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// A pool closed on a tariff reaches its users at once: the subscription loses the pool's
// inbound and the user's pools say so; opened again, both come back. A user closed by
// hand on another pool keeps it.
func TestTariffClosesPool(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	q := h.st.Q
	premium, _ := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "Premium", CreatedAt: 1})
	other, _ := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "Other", CreatedAt: 1})
	ins, _ := q.ListInbounds(ctx)
	if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: sql.NullInt64{Int64: premium.ID, Valid: true}, ID: ins[0].ID}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	tariff := tariffs[1]
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariff.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	userPools := "/" + adminPath + "/api/v1/users/" + strconv.FormatInt(u.ID, 10) + "/pools"
	// The admin closes the other pool for this user alone.
	if resp, body := h.do(http.MethodPut, userPools, map[string]any{"pools": []map[string]any{{"pool_id": other.ID, "traffic_limit": nil, "excluded": true}}}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("close per user: %d %s", resp.StatusCode, body)
	}
	sub := func() string {
		t.Helper()
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.32"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscription: %d", resp.StatusCode)
		}
		return string(body)
	}
	// One proxy per inbound on the one node: the count says whether the pool's is there.
	proxies := func() int { return strings.Count(sub(), `"server": `) }
	all := proxies()
	if all == 0 {
		t.Fatal("no proxies")
	}
	excluded := func() map[int64]bool {
		t.Helper()
		_, body := h.do(http.MethodGet, userPools, nil, nil)
		var ps []struct {
			PoolID   int64 `json:"pool_id"`
			Excluded bool  `json:"excluded"`
		}
		if err := json.Unmarshal(body, &ps); err != nil {
			t.Fatalf("%v %s", err, body)
		}
		out := map[int64]bool{}
		for _, p := range ps {
			out[p.PoolID] = p.Excluded
		}
		return out
	}
	put := func(pools []map[string]any) {
		t.Helper()
		body := map[string]any{"name": tariff.Name, "duration_days": tariff.DurationDays, "traffic_limit": tariff.TrafficLimit.Int64, "reset_strategy": tariff.ResetStrategy, "pools": pools}
		if resp, b := h.do(http.MethodPut, "/"+adminPath+"/api/v1/tariffs/"+strconv.FormatInt(tariff.ID, 10), body, csrf); resp.StatusCode != http.StatusOK {
			t.Fatalf("tariff: %d %s", resp.StatusCode, b)
		}
	}

	put([]map[string]any{{"pool_id": premium.ID, "traffic_limit": nil, "excluded": true}})
	if ex := excluded(); !ex[premium.ID] || !ex[other.ID] {
		t.Fatalf("closed for the tariff's user at once, the per-user one kept: %v", ex)
	}
	if n := proxies(); n != all-1 {
		t.Fatalf("the closed pool's inbound must leave the subscription: %d of %d", n, all)
	}
	_, body := h.do(http.MethodGet, "/"+adminPath+"/api/v1/tariffs", nil, nil)
	if !strings.Contains(string(body), `"excluded":true`) {
		t.Fatalf("the tariff shows the closed pool: %s", body)
	}

	put([]map[string]any{})
	if ex := excluded(); ex[premium.ID] || !ex[other.ID] {
		t.Fatalf("opened again for the tariff's user, the per-user one kept: %v", ex)
	}
	if n := proxies(); n != all {
		t.Fatalf("the pool's inbound is back: %d of %d", n, all)
	}
}
