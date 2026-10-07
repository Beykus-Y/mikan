package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// packageEnv: a subscription of buyer 555 on the sale tariff (150 GB) and a 50 GB package
// for its main traffic.
func packageEnv(t *testing.T) (*env, db.User, db.TrafficPackage) {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "mine", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: 1}))
	pk, err := domain.NewPackages(e.st, func() time.Time { return e.now }).Create(ctx, domain.PackageInput{Name: "+50 ГБ", Bytes: 50 * domain.GiB,
		Lifetime: domain.LifetimeUsed, PriceStars: sql.NullInt64{Int64: 75, Valid: true}, PriceRub: sql.NullInt64{Int64: 7900, Valid: true}, OnSale: true})
	must(t, err)
	return e, u, pk
}

func TestDiscountedPackageInvoicePersistsAmountAndReusesInvoice(t *testing.T) {
	e, u, pk := packageEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "PK25", Type: "percent", Value: 25, Currency: "XTR", PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	req := PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: Stars, PromoCode: pc.Code}
	p, err := e.s.PackageInvoice(ctx, req)
	must(t, err)
	if p.Amount != 57 {
		t.Fatalf("invoice amount = %d, want 57", p.Amount)
	}
	if stored := e.payment(p.ID); stored.Amount != 57 {
		t.Fatalf("stored amount = %d, want discounted 57", stored.Amount)
	}
	calls := e.tg.invoiceCount()
	if again, err := e.s.PackageInvoice(ctx, req); err != nil || again.ID != p.ID || e.tg.invoiceCount() != calls {
		t.Fatalf("invoice was not reused: payment=%+v err=%v calls=%d->%d", again, err, calls, e.tg.invoiceCount())
	}
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 57))
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-discount-pkg", "XTR", 57))
	if e.payment(p.ID).Status != "applied" {
		t.Fatalf("discounted payment not applied: %+v", e.payment(p.ID))
	}
}

func TestDiscountedPackagePreCheckoutHonorsReservationExpiry(t *testing.T) {
	e, u, pk := packageEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "SHORT", Type: "percent", Value: 10, Currency: "XTR", PerUserLimit: 1, DiscountTtl: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: Stars, PromoCode: pc.Code})
	must(t, err)
	e.now = e.now.Add(2 * time.Second)
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", p.Amount); !errors.Is(err, ErrBadPayment) {
		t.Fatalf("expired discounted package passed pre-checkout: %v", err)
	}
}

// Buying a package with Stars gives the subscription one grant, however many times and
// how concurrently Telegram reports the payment.
func TestStarsPackage(t *testing.T) {
	e, u, pk := packageEnv(t)
	ctx := context.Background()
	offers, _, err := e.s.PackageOffers(ctx, u.ID)
	if err != nil || len(offers) != 1 || offers[0].Stars != 75 || offers[0].Rub != 0 { // no adapter takes rubles here
		t.Fatalf("offers: %+v %v", offers, err)
	}
	if _, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 556, UserID: u.ID, PackageID: pk.ID, Provider: Stars}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("someone else's subscription: %v", err)
	}
	p, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: Stars})
	must(t, err)
	if p.Kind != KindPackage || p.Amount != 75 || p.Currency != "XTR" || p.PackageID.Int64 != pk.ID || p.TariffID.Valid || p.TariffName != "+50 ГБ" || p.PayUrl == "" {
		t.Fatalf("invoice: %+v", p)
	}
	if again, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: Stars}); err != nil || again.ID != p.ID {
		t.Fatalf("a second invoice for the same purchase: %+v %v", again, err)
	}
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 75))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.s.StarsPaid(ctx, 555, p.Payload, "ch-p", "XTR", 75)
		}()
	}
	wg.Wait()
	_ = e.s.StarsPaid(ctx, 555, p.Payload, "ch-q", "XTR", 75)
	got := e.payment(p.ID)
	if got.Status != "applied" || got.UserID.Int64 != u.ID {
		t.Fatalf("payment: %+v", got)
	}
	gs, err := e.st.Q.ListUserGrants(ctx, u.ID)
	must(t, err)
	if len(gs) != 1 || gs[0].Bytes != 50*domain.GiB || gs[0].Source != domain.SourcePurchase || gs[0].PaymentID.Int64 != p.ID || gs[0].PackageID.Int64 != pk.ID {
		t.Fatalf("grants: %+v", gs)
	}
	if e.tg.told() != 1 || e.tg.paid[0].Kind != KindPackage {
		t.Fatalf("told %d", e.tg.told())
	}
	// The subscription itself is as it was: no renewal, no new user.
	after, _ := e.st.Q.GetUser(ctx, u.ID)
	if after.ExpiresAt != u.ExpiresAt || after.PeriodStart != u.PeriodStart {
		t.Fatalf("the package changed the subscription: %+v", after)
	}
	// Applying again (a reconcile pass) changes nothing.
	must(t, e.s.Apply(ctx, p.ID))
	if gs, _ := e.st.Q.ListUserGrants(ctx, u.ID); len(gs) != 1 {
		t.Fatalf("applied twice: %d grants", len(gs))
	}
}

// Only packages on sale, for a quota the subscription has, can be bought; one taken off
// sale after the invoice fails Telegram's pre-checkout but a paid one still applies.
func TestPackageNotForSale(t *testing.T) {
	e, u, pk := packageEnv(t)
	ctx := context.Background()
	q := e.st.Q
	buy := func(id int64) error {
		_, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: id, Provider: Stars})
		return err
	}
	off, err := q.CreateTrafficPackage(ctx, db.CreateTrafficPackageParams{Name: "off", Bytes: domain.GiB, Lifetime: domain.LifetimeUsed,
		PriceStars: sql.NullInt64{Int64: 1, Valid: true}, OnSale: 0, CreatedAt: 1})
	must(t, err)
	if err := buy(off.ID); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("not on sale: %v", err)
	}
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	must(t, err)
	wl, err := q.CreateTrafficPackage(ctx, db.CreateTrafficPackageParams{Name: "wl", Bytes: domain.GiB, PoolID: sql.NullInt64{Int64: pool.ID, Valid: true},
		Lifetime: domain.LifetimeUsed, PriceStars: sql.NullInt64{Int64: 1, Valid: true}, OnSale: 1, CreatedAt: 1})
	must(t, err)
	if err := buy(wl.ID); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("a pool the subscription has no limit in: %v", err)
	}
	if err := buy(999); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("unknown package: %v", err)
	}
	if _, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: "paypal"}); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("unknown provider: %v", err)
	}

	p, err := e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: pk.ID, Provider: Stars})
	must(t, err)
	must(t, domain.NewPackages(e.st, func() time.Time { return e.now }).Archive(ctx, pk.ID))
	if err := buy(pk.ID); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("archived: %v", err)
	}
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 75); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("pre-checkout of an archived package: %v", err)
	}
	// Paid before it was archived (the provider took the money): it applies.
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-a", "XTR", 75))
	if gs, _ := q.ListUserGrants(ctx, u.ID); len(gs) != 1 || e.payment(p.ID).Status != "applied" {
		t.Fatalf("a paid package must apply: %+v", gs)
	}
}

// A package for a pool closed on the subscription's tariff is neither invoiced nor let
// through Telegram's pre-checkout; one already paid still applies, with a warning.
func TestPackageClosedPoolIsNotSold(t *testing.T) {
	e, u, _ := packageEnv(t)
	ctx := context.Background()
	q := e.st.Q
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	must(t, err)
	wl, err := q.CreateTrafficPackage(ctx, db.CreateTrafficPackageParams{Name: "wl", Bytes: domain.GiB, PoolID: sql.NullInt64{Int64: pool.ID, Valid: true},
		Lifetime: domain.LifetimeUsed, PriceStars: sql.NullInt64{Int64: 5, Valid: true}, OnSale: 1, CreatedAt: 1})
	must(t, err)
	setPool := func(limit sql.NullInt64, closed bool) {
		t.Helper()
		must(t, q.SetUserPoolLimit(ctx, db.SetUserPoolLimitParams{UserID: u.ID, PoolID: pool.ID, TrafficLimit: limit, Excluded: closed}))
	}
	buy := func() (db.Payment, error) {
		return e.s.PackageInvoice(ctx, PackageRequest{TgID: 555, UserID: u.ID, PackageID: wl.ID, Provider: Stars})
	}
	limit := sql.NullInt64{Int64: 10 * domain.GiB, Valid: true}

	setPool(limit, false)
	p, err := buy()
	must(t, err)
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 5))

	// Closed after the invoice: Telegram's pre-checkout refuses, a new invoice is refused.
	setPool(sql.NullInt64{}, true)
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 5); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("pre-checkout for a closed pool: %v", err)
	}
	if _, err := buy(); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("invoice for a closed pool: %v", err)
	}
	// A limit row left on a closed pool does not make it sellable.
	setPool(limit, true)
	if _, err := buy(); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("invoice for a closed pool with a limit row: %v", err)
	}
	if ups, _ := q.ListUserPools(ctx, u.ID); domain.PackageFits(u, ups, wl) {
		t.Fatal("PackageFits must refuse a closed pool")
	}
	if offers, _, err := e.s.PackageOffers(ctx, u.ID); err != nil {
		t.Fatal(err)
	} else {
		for _, o := range offers {
			if o.Package.ID == wl.ID {
				t.Fatal("a closed pool's package is offered")
			}
		}
	}

	// Paid before it was closed (the provider took the money): it applies and warns.
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-closed", "XTR", 5))
	if gs, _ := q.ListUserGrants(ctx, u.ID); len(gs) != 1 || e.payment(p.ID).Status != "applied" {
		t.Fatalf("a paid package must apply: %+v", gs)
	}
	if !strings.Contains(e.logs.String(), "closed for the subscription") {
		t.Errorf("no warning in the log: %s", e.logs)
	}

	// Opened again: sellable.
	setPool(limit, false)
	if _, err := buy(); err != nil {
		t.Fatalf("an open pool sells: %v", err)
	}
}
