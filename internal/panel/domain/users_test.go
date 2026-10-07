package domain

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type changes struct{ policies, slots int }

func (c *changes) PoliciesChanged() { c.policies++ }
func (c *changes) SlotsChanged()    { c.slots++ }

func setup(t *testing.T, now *time.Time) (*store.Store, *Users, *changes) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := Seed(ctx, st, *now); err != nil {
		t.Fatal(err)
	}
	ch := &changes{}
	clock := func() time.Time { return *now }
	return st, NewUsers(st, NewPool(st, clock), ch, clock), ch
}

func TestState(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) sql.NullInt64 { return sql.NullInt64{Int64: now.Add(d).Unix(), Valid: true} }
	limit := sql.NullInt64{Int64: 100, Valid: true}
	cases := []struct {
		name   string
		u      db.User
		grants int64
		want   string
	}{
		{"no limits", db.User{Status: "active"}, 0, StateActive},
		{"disabled wins", db.User{Status: "disabled", ExpiresAt: at(-time.Hour)}, 0, StateDisabled},
		{"expired", db.User{Status: "active", ExpiresAt: at(-time.Second)}, 0, StateExpired},
		{"expires exactly now", db.User{Status: "active", ExpiresAt: at(0)}, 0, StateExpired},
		{"over quota", db.User{Status: "active", TrafficLimit: limit, UsedUp: 40, UsedDown: 60}, 0, StateLimited},
		{"over quota with grants left", db.User{Status: "active", TrafficLimit: limit, UsedUp: 40, UsedDown: 70}, 1, StateActive},
		{"under quota", db.User{Status: "active", TrafficLimit: limit, UsedDown: 99}, 0, StateActive},
		{"expiring in 7d", db.User{Status: "active", ExpiresAt: at(7 * 24 * time.Hour)}, 0, StateExpiring},
		{"8 days left", db.User{Status: "active", ExpiresAt: at(8 * 24 * time.Hour)}, 0, StateActive},
	}
	for _, c := range cases {
		if got := State(c.u, c.grants, now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestCreateCopiesTariffAndTakesSlot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1] // Стандарт: 150 GiB, 30 days, 3 devices, period reset
	u, err := users.Create(ctx, CreateInput{Name: "  Анна ", TariffID: std.ID, Tags: []string{"tg", " "}})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Анна" || u.TrafficLimit != std.TrafficLimit || u.DeviceLimit != std.DeviceLimit || u.ResetStrategy != "period" {
		t.Fatalf("tariff not applied: %+v", u)
	}
	if !u.ExpiresAt.Valid || u.ExpiresAt.Int64 != now.Add(30*24*time.Hour).Unix() {
		t.Fatalf("expiry = %v", u.ExpiresAt)
	}
	if len(u.SubToken) != 24 || !u.SlotID.Valid {
		t.Fatalf("credentials: token %q slot %v", u.SubToken, u.SlotID)
	}
	if got := DecodeTags(u.Tags); len(got) != 1 || got[0] != "tg" {
		t.Fatalf("tags = %v", got)
	}
	slot, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	if slot.State != "assigned" {
		t.Fatalf("slot state %s", slot.State)
	}
	if ch.policies != 1 || ch.slots != 0 {
		t.Fatalf("changes = %+v, want one policy push and no listener change", ch)
	}
	if _, err := users.Create(ctx, CreateInput{Name: "x", TariffID: 999}); err == nil {
		t.Fatal("unknown tariff accepted")
	}
}

func TestExtend(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	u, _ = users.Extend(ctx, u.ID, 30)
	if want := now.Add(60 * 24 * time.Hour).Unix(); u.ExpiresAt.Int64 != want {
		t.Fatalf("active user: expiry %d, want %d (added to the current term)", u.ExpiresAt.Int64, want)
	}
	now = now.Add(100 * 24 * time.Hour)
	u, _ = users.Extend(ctx, u.ID, 30)
	if want := now.Add(30 * 24 * time.Hour).Unix(); u.ExpiresAt.Int64 != want {
		t.Fatalf("expired user: expiry %d, want %d (counted from now)", u.ExpiresAt.Int64, want)
	}
}

func TestReissueBurnsOldSlot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, _ := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[0].ID})
	oldSlot, oldToken := u.SlotID.Int64, u.SubToken
	u, err := users.Reissue(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.SlotID.Int64 == oldSlot || u.SubToken == oldToken {
		t.Fatal("credentials not replaced")
	}
	s, _ := st.Q.GetSlot(ctx, oldSlot)
	if s.State != "burned" {
		t.Fatalf("old slot %s, want burned", s.State)
	}
	if _, err := st.Q.GetUserBySubToken(ctx, oldToken); err == nil {
		t.Fatal("old subscription token still resolves")
	}
}

func TestDeleteBurnsSlotAndPurgeKeepsAssigned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	a, _ := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[0].ID})
	b, _ := users.Create(ctx, CreateInput{Name: "b", TariffID: tariffs[0].ID})
	if err := users.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	pool := NewPool(st, func() time.Time { return now })
	if err := pool.PurgeBurned(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.GetSlot(ctx, a.SlotID.Int64); err == nil {
		t.Fatal("burned slot survived purge")
	}
	if s, err := st.Q.GetSlot(ctx, b.SlotID.Int64); err != nil || s.State != "assigned" {
		t.Fatalf("assigned slot damaged: %v %v", s, err)
	}
}

func TestNextReset(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	if r, _ := NextReset(db.User{ResetStrategy: "month_start"}, now); !r.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month_start: %v", r)
	}
	start := now.Add(-10 * 24 * time.Hour).Unix()
	if r, _ := NextReset(db.User{ResetStrategy: "period", PeriodStart: start, PeriodDays: 30}, now); r.Unix() != start+30*86400 {
		t.Fatalf("period: %v", r)
	}
	if _, ok := NextReset(db.User{ResetStrategy: "none"}, now); ok {
		t.Fatal("none must not reset")
	}
}

// The counts the database makes agree with State user by user, on the boundaries too.
func TestCountStatesMatchesState(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	set := func(id int64, change string, args ...any) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, "UPDATE users SET "+change+" WHERE id = "+strconv.FormatInt(id, 10), args...); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(id int64, g GrantSpec, at time.Time) {
		t.Helper()
		u, _ := st.Q.GetUser(ctx, id)
		if _, err := GrantTx(ctx, st.Q, u, g, at); err != nil {
			t.Fatal(err)
		}
	}
	week := int64(7 * 24 * 3600)
	for i, change := range []func(id int64){
		func(int64) {}, // active, the term a month away
		func(id int64) { set(id, "status = 'disabled'") },
		func(id int64) { set(id, "expires_at = $1", now.Unix()) },                       // expired at this very second
		func(id int64) { set(id, "expires_at = $1", now.Unix()+week) },                  // expiring: exactly a week left
		func(id int64) { set(id, "expires_at = $1", now.Unix()+week+1) },                // active: a second more
		func(id int64) { set(id, "expires_at = NULL") },                                 // active: no end
		func(id int64) { set(id, "traffic_limit = 100, used_up = 60, used_down = 40") }, // limited
		func(id int64) { // past the base with a main grant: not limited
			set(id, "traffic_limit = 100, used_up = 100")
			grant(id, GrantSpec{Bytes: 10, Lifetime: LifetimeUsed, Source: SourceAdmin}, now)
		},
		func(id int64) { // its grant expired: limited
			set(id, "traffic_limit = 100, used_up = 100")
			grant(id, GrantSpec{Bytes: 10, Lifetime: LifetimeDays, Days: 1, Source: SourceAdmin}, now.Add(-48*time.Hour))
		},
	} {
		u, err := users.Create(ctx, CreateInput{Name: "u" + strconv.Itoa(i), TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		change(u.ID)
	}
	all, _ := st.Q.ListUsers(ctx)
	grants, _ := LoadGrantsLeft(ctx, st.Q, now)
	want := db.CountUserStatesRow{Total: int64(len(all))}
	for _, u := range all {
		switch State(u, grants.Main(u.ID), now) {
		case StateActive:
			want.Active++
		case StateExpiring:
			want.Expiring++
		case StateLimited:
			want.Limited++
		case StateExpired:
			want.Expired++
		case StateDisabled:
			want.Disabled++
		}
	}
	got, err := CountStates(ctx, st.Q, now)
	if err != nil || got != want {
		t.Fatalf("counts %+v (%v), State says %+v", got, err, want)
	}
	if want.Active != 4 || want.Expiring != 1 || want.Limited != 2 || want.Expired != 1 || want.Disabled != 1 {
		t.Fatalf("the cases do not cover every state: %+v", want)
	}
}
