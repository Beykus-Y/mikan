package subs

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

func TestTitleValuesHoldTitleVars(t *testing.T) {
	vars := titleValues(db.User{}, 0, Config{}, time.Now())
	if len(vars) != len(TitleVars) {
		t.Fatalf("%d values, %d variables", len(vars), len(TitleVars))
	}
	for _, name := range TitleVars {
		if _, ok := vars[name]; !ok {
			t.Errorf("no value for {%s}", name)
		}
	}
}

func TestProfileTitle(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cfg := Config{Brand: "Mikan"}
	for _, tc := range []struct{ title, name, want string }{
		{"", "Вася", "Mikan"},
		{"{name}", "Вася", "Вася"},
		{"{name}", "", "Mikan"},
		{"{name}", "   ", "Mikan"},
		{" {name} ", "", "Mikan"},
		{"{brand} {name}", "", "Mikan"},
		{"{nope}", "Вася", "{nope}"},
	} {
		cfg.Title = tc.title
		if got := profileTitle(cfg, titleValues(db.User{Name: tc.name}, 0, cfg, now)); got != tc.want {
			t.Errorf("title %q, name %q: %q, want %q", tc.title, tc.name, got, tc.want)
		}
	}
}

func TestFillTitle(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	u := db.User{Name: "Вася {brand}", UsedUp: 1 << 30, UsedDown: 4 << 30,
		ExpiresAt:    sql.NullInt64{Int64: now.Add(36 * time.Hour).Unix(), Valid: true},
		TrafficLimit: sql.NullInt64{Int64: 50 << 30, Valid: true}}
	vars := titleValues(u, 10<<30, Config{Brand: "Mikan"}, now)
	got := fillTitle("{brand} · {name} · до {date} ({days} дн.) · {used}/{total}, осталось {left} {nope}", vars)
	want := "Mikan · Вася {brand} · до 06.10.2026 (2 дн.) · 5 ГБ/60 ГБ, осталось 55 ГБ {nope}"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	free := titleValues(db.User{Name: "a"}, 0, Config{Brand: "M", Lang: "en"}, now)
	if got := fillTitle("{date} {days} {left} {total} {used}", free); got != "∞ ∞ ∞ ∞ 0 B" {
		t.Fatalf("no term, no limit: %q", got)
	}
	expired := titleValues(db.User{ExpiresAt: sql.NullInt64{Int64: now.Add(-time.Hour).Unix(), Valid: true}}, 0, Config{}, now)
	if expired["days"] != "0" {
		t.Fatalf("an ended term has 0 days left: %q", expired["days"])
	}
	long := fillTitle(strings.Repeat("я", 150)+"{name}", map[string]string{"name": strings.Repeat("ю", 100) + "\n"})
	if n := len([]rune(long)); n != TitleMax || strings.ContainsAny(long, "\n") {
		t.Fatalf("one line of %d letters at most: %d", TitleMax, n)
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"Mikan · Вася":   "Mikan · Вася",
		"VPN/RU: 30 дн.": "VPN RU  30 дн.",
		`a\b*c?"d"<e>|`:  "a b c  d  e",
		" \t\n":          "mikan",
		"":               "mikan",
	} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}
