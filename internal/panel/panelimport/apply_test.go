package panelimport

import (
	"context"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/storetest"
)

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

type countChanges struct{ policies int }

func (c *countChanges) PoliciesChanged() { c.policies++ }
func (c *countChanges) SlotsChanged()    {}

func seeded(t *testing.T) (*store.Store, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := domain.Seed(ctx, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	tariffs, err := st.Q.ListTariffs(ctx)
	if err != nil || len(tariffs) < 2 {
		t.Fatal(err)
	}
	return st, tariffs[1].ID
}

// A step that fails after the user is made takes the whole user back: a second import
// then makes it, instead of skipping a half-made user as taken.
func TestApplyIsAllOrNothingPerUser(t *testing.T) {
	st, tariff := seeded(t)
	ctx := context.Background()
	now := time.Now()
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	list := []User{
		{Name: "first", Status: StatusActive},
		{Name: "broken", Status: StatusDisabled, TrafficLimit: 100},
		{Name: "third", Status: StatusActive},
	}
	afterCreate = func(name string) error {
		if name == "broken" {
			return errors.New("the disk is full")
		}
		return nil
	}
	defer func() { afterCreate = nil }()
	r, err := Apply(ctx, st, users, now, Marzban, tariff, list, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Created != 2 || r.Failed.Count != 1 || r.Links != 2 {
		t.Fatalf("report %+v", r)
	}
	if taken, _ := st.Q.UserNameTaken(ctx, "broken"); taken {
		t.Fatal("a half-made user is left in the database")
	}
	afterCreate = nil
	r, err = Apply(ctx, st, users, now, Marzban, tariff, list, nil)
	if err != nil || r.Created != 1 || r.Skipped.Count != 2 {
		t.Fatalf("second import %+v %v", r, err)
	}
	list2, _ := st.Q.ListUsers(ctx)
	for _, u := range list2 {
		if u.Name == "broken" && (u.Status != "disabled" || !u.TrafficLimit.Valid || u.TrafficLimit.Int64 != 100) {
			t.Fatalf("broken after the retry: %+v", u)
		}
	}
}

// The nodes are told once, at the end, not twice per user; links that were there already
// are not counted again.
func TestApplyTellsTheNodesOnce(t *testing.T) {
	st, tariff := seeded(t)
	ctx := context.Background()
	changes := &countChanges{}
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), changes, time.Now)
	var list []User
	for _, n := range []string{"a", "b", "c", "d"} {
		list = append(list, User{Name: n, Status: StatusActive, Token: "tok-" + n})
	}
	var progress []int
	r, err := Apply(ctx, st, users, time.Now(), Remnawave, tariff, list, func(done int) { progress = append(progress, done) })
	if err != nil || r.Created != 4 || r.Links != 4 {
		t.Fatalf("report %+v %v", r, err)
	}
	if changes.policies != 1 {
		t.Fatalf("the nodes were told %d times", changes.policies)
	}
	if len(progress) != 4 || progress[3] != 4 {
		t.Fatalf("progress %v", progress)
	}
	// A token another user has already is not taken over, and not counted.
	r, err = Apply(ctx, st, users, time.Now(), Remnawave, tariff, []User{{Name: "e", Status: StatusActive, Token: "tok-a"}}, nil)
	if err != nil || r.Created != 1 || r.Links != 0 {
		t.Fatalf("a taken token: %+v %v", r, err)
	}
}

// Users mikan would not take are reported, the rest come over.
func TestApplyReportsInvalidUsers(t *testing.T) {
	st, tariff := seeded(t)
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	r, err := Apply(context.Background(), st, users, time.Now(), Marzban, tariff, []User{
		{Name: "", Status: StatusActive},
		{Name: "ok", Status: StatusActive, Expires: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Name: "fine", Status: StatusActive},
	}, nil)
	if err != nil || r.Created != 1 || r.Failed.Count != 2 {
		t.Fatalf("report %+v %v", r, err)
	}
}
