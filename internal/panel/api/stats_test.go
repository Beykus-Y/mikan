package api

import (
	"context"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/storetest"
)

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

// A user online from two devices with keys of their own is one user online.
func TestOverviewCountsUsersNotDevices(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := st.Q.ListTariffs(ctx)
	pool := domain.NewPool(st, clock)
	u, err := domain.NewUsers(st, pool, noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	phone, err := domain.NewDevices(st, pool, noChanges{}, clock).Bind(ctx, u, domain.DeviceInfo{HWID: "phone-0123456789"}, false)
	if err != nil {
		t.Fatal(err)
	}
	own, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	h := &handlers{d: Deps{Store: st, Now: clock, Online: func() map[string]nodeapi.Online {
		return map[string]nodeapi.Online{
			own.Name:   {Conns: 1, IPs: []string{"198.51.100.1"}},
			phone.Name: {Conns: 2, IPs: []string{"198.51.100.2"}},
		}
	}}}
	out, err := h.overview(ctx, nil)
	if err != nil || out.Body.Online != 1 {
		t.Fatalf("online users: %d %v", out.Body.Online, err)
	}
}
