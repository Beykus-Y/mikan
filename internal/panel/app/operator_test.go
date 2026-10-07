package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The announcement and the app branding over the API: bad values are refused by field,
// and the next subscription carries what was saved.
func TestOperatorHeadersOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355, settings.KeyBrand: "Mikan"} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1/settings"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}

	resp, body := h.do(http.MethodPatch, api, map[string]any{"brand_accent": "orange", "brand_logo_url": "http://x.example/l.png", "sub_announce_url": "ftp://x", "sub_announce": "a\nb"}, csrf)
	for _, field := range []string{"brand_accent", "brand_logo_url", "sub_announce_url", "sub_announce"} {
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "body."+field) {
			t.Fatalf("%s must be refused: %d %s", field, resp.StatusCode, body)
		}
	}

	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func() http.Header {
		t.Helper()
		resp, _ := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.32 ClashFest/1.2.0"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscription: %d", resp.StatusCode)
		}
		return resp.Header
	}
	hd := fetch()
	if hd.Get("X-Branding-Enabled") != "" || hd.Get("Announce") != "" {
		t.Fatalf("off by default: %v", hd)
	}
	if !strings.HasSuffix(hd.Get("Profile-Web-Page-Url"), "/"+subPath+"/"+u.SubToken) {
		t.Errorf("page: %q", hd.Get("Profile-Web-Page-Url"))
	}

	resp, body = h.do(http.MethodPatch, api, map[string]any{"app_branding": true, "brand_accent": "#F07A2E", "brand_logo_url": "https://cdn.example.com/logo.png",
		"sub_announce": "Работы ночью", "sub_announce_url": "https://t.me/news"}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"app_branding":true`) {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	hd = fetch()
	for k, v := range map[string]string{"X-Branding-Enabled": "true", "X-Brand-Desktop-Enabled": "true", "X-Brand-Name": "Mikan", "X-Brand-Accent-Color": "#F07A2E",
		"X-Brand-Desktop-Logo-URL": "https://cdn.example.com/logo.png", "Announce-Url": "https://t.me/news"} {
		if hd.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, hd.Get(k), v)
		}
	}
	if !strings.HasPrefix(hd.Get("Announce"), "base64:") {
		t.Errorf("announce: %q", hd.Get("Announce"))
	}

	decode := func(v string) string {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "base64:"))
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		return string(raw)
	}
	// The title and the announcement with variables: filled per user; a {word} that is no
	// variable saves and stays as text, so an old announcement does not block the card.
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"sub_title": "{brand} {nmae}", "sub_announce": "Работы {ночью}"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("an unknown variable must save: %d %s", resp.StatusCode, body)
	}
	hd = fetch()
	if got := decode(hd.Get("Profile-Title")); got != "Mikan {nmae}" {
		t.Errorf("unknown variable stays text: %q", got)
	}
	if got := decode(hd.Get("Announce")); got != "Работы {ночью}" {
		t.Errorf("announce keeps its text: %q", got)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"sub_title": "{brand} · {name}", "sub_announce": "Осталось {left} до {date}"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("save the title: %d %s", resp.StatusCode, body)
	}
	hd = fetch()
	if got := decode(hd.Get("Profile-Title")); got != "Mikan · a" {
		t.Errorf("title: %q", got)
	}
	// Clash apps name the profile after the file, so the file carries the title too.
	if got, want := hd.Get("Content-Disposition"), "attachment; filename*=UTF-8''Mikan%20%C2%B7%20a.yaml"; got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if got := decode(hd.Get("Announce")); !strings.HasPrefix(got, "Осталось ") || strings.Contains(got, "{") {
		t.Errorf("announce: %q", got)
	}

	// With nothing to serve the app gets the stub, which is not about devices: v2RayTun
	// must not show a device limit notice.
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE inbounds SET enabled = 0"); err != nil {
		t.Fatal(err)
	}
	resp, _ = h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "v2RayTun/1.0"})
	if resp.Header.Get("X-Hwid-Limit") != "" || resp.Header.Get("X-Hwid-Max-Devices-Reached") != "" {
		t.Errorf("a stub without servers speaks of devices: %v", resp.Header)
	}
}

func TestSubscriptionPageCustomizationOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	clock := func() time.Time { return h.now }
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "brand user", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	modules := append([]settings.SubscriptionModule(nil), settings.DefaultSubscriptionModules...)
	modules[0], modules[1] = modules[1], modules[0]
	modules[0].Enabled = false
	resp, body := h.do(http.MethodPatch, "/"+adminPath+"/api/v1/settings", map[string]any{
		"subscription_theme": "ocean", "subscription_logo": "🌿", "subscription_modules": modules,
	}, map[string]string{"X-CSRF-Token": h.csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save subscription-page settings: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"/info", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscription info: %d %s", resp.StatusCode, body)
	}
	var info struct {
		Theme   string                        `json:"theme"`
		Logo    string                        `json:"logo"`
		Modules []settings.SubscriptionModule `json:"modules"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.Theme != "ocean" || info.Logo != "🌿" || len(info.Modules) != len(modules) || info.Modules[0].ID != "usage" || info.Modules[0].Enabled {
		t.Fatalf("subscription customization did not reach the page: %+v", info)
	}
}
