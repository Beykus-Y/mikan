package app

import (
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

// legacyCard is the part of a user's card that tells about the links from the old panel.
type legacyCard struct {
	Legacy *struct {
		Source string `json:"source"`
		Active bool   `json:"active"`
		URL    string `json:"url"`
	} `json:"legacy"`
}

func userCard(t *testing.T, h *harness, api string, id int64) (legacyCard, string) {
	t.Helper()
	resp, body := h.do(http.MethodGet, api+"/users/"+strconv.FormatInt(id, 10), nil, map[string]string{"X-CSRF-Token": h.csrf})
	var c legacyCard
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &c) != nil {
		t.Fatalf("user %d: %d %s", id, resp.StatusCode, body)
	}
	return c, string(body)
}

// The card shows the old links of an imported user: Remnawave's is a whole address, as it
// is the one token the old panel handed out; Marzban's and PasarGuard's tokens are signed
// per request and were never seen, so the card says that the old links work, with no
// address made up. A reissue stops them and takes the note off.
func TestUserCardShowsOldLinks(t *testing.T) {
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
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	old := fakeRemnawave(t, []map[string]any{
		{"id": 1, "shortUuid": "Abc123Def456Ghi7", "username": "masha", "status": "ACTIVE", "trafficLimitBytes": 0, "expireAt": "2099-12-31T00:00:00.000Z"},
	})
	if r := runImport(t, h, api, csrf, map[string]any{"kind": "remnawave", "url": old.URL, "token": "rw-token", "tariff_id": tariffs[1].ID}); r.Created != 1 {
		t.Fatalf("import: %+v", r)
	}
	list, _ := h.st.Q.ListUsers(ctx)
	masha := list[len(list)-1]
	// The old links are not set up yet: the user has them, but they do not open.
	if c, body := userCard(t, h, api, masha.ID); c.Legacy == nil || c.Legacy.Source != "remnawave" || c.Legacy.Active || c.Legacy.URL != "" {
		t.Fatalf("old links not set up: %s", body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"path": "api/sub"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy path: %d %s", resp.StatusCode, body)
	}
	c, body := userCard(t, h, api, masha.ID)
	if c.Legacy == nil || c.Legacy.Source != "remnawave" || !c.Legacy.Active || c.Legacy.URL != "https://203.0.113.10:21355/api/sub/Abc123Def456Ghi7" {
		t.Fatalf("remnawave old link: %s", body)
	}

	// A user with only signed links: no address, and active only with the secret.
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "olga", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name:olga", "id:42"} {
		if _, err := h.st.Q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: k, UserID: u.ID, Source: "pasarguard"}); err != nil {
			t.Fatal(err)
		}
	}
	if c, body := userCard(t, h, api, u.ID); c.Legacy == nil || c.Legacy.Source != "pasarguard" || c.Legacy.Active || c.Legacy.URL != "" {
		t.Fatalf("signed links without the secret: %s", body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"secret": "s3cr3t-key-from-jwt-table"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy secret: %d %s", resp.StatusCode, body)
	}
	c, body = userCard(t, h, api, u.ID)
	if c.Legacy == nil || !c.Legacy.Active || c.Legacy.URL != "" || strings.Contains(body, "name:olga") || strings.Contains(body, "id:42") {
		t.Fatalf("signed old links: %s", body)
	}

	// A user who never had any has no note; a reissue takes it off.
	plain, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "plain", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if c, body := userCard(t, h, api, plain.ID); c.Legacy != nil {
		t.Fatalf("a user with no old links has a note: %s", body)
	}
	if resp, body := h.do(http.MethodPost, api+"/users/"+strconv.FormatInt(masha.ID, 10)+"/reissue", nil, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("reissue: %d %s", resp.StatusCode, body)
	}
	if c, body := userCard(t, h, api, masha.ID); c.Legacy != nil {
		t.Fatalf("reissued, yet old links are shown: %s", body)
	}
}

// A read key does not get the old links, as it does not get the user's own: the address
// holds a token that opens the subscription.
func TestReadKeyDoesNotGetOldLinks(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	clock := func() time.Time { return k.now }
	u, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "masha", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	const token = "Abc123Def456Ghi7"
	if _, err := k.st.Q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: token, UserID: u.ID, Source: "remnawave"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settings.New(k.st.Q), settings.KeyLegacySubPath, "api/sub"); err != nil {
		t.Fatal(err)
	}
	path := "/users/" + idOf(u.ID)
	resp, body := k.asKey(k.read, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), token) || strings.Contains(string(body), `"legacy"`) {
		t.Errorf("a read key sees the old link: %d %s", resp.StatusCode, body)
	}
	resp, body = k.asKey(k.full, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "https://203.0.113.10:21355/api/sub/"+token) {
		t.Errorf("a full key does not see the old link: %d %s", resp.StatusCode, body)
	}
}
