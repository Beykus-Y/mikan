package api

import (
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

func TestValidatePromoBody(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	base := PromoBody{Code: "SAVE", Type: "percent", Value: 20, PerUserLimit: 1}
	cases := []struct {
		name string
		in   PromoBody
		used int64
		want string
	}{
		{name: "past end", in: func() PromoBody { x := base; v := now.Unix() - 1; x.EndsAt = &v; return x }(), want: "ends_at_past"},
		{name: "max uses below used", in: func() PromoBody { x := base; v := int64(2); x.MaxUses = &v; return x }(), used: 3, want: "max_uses_below_used"},
		{name: "bad percent", in: func() PromoBody { x := base; x.Value = 101; return x }(), want: "bad_percent"},
		{name: "traffic above grant maximum", in: func() PromoBody { x := base; x.Type = "traffic"; x.Value = domain.MaxGrantBytes + 1; return x }(), want: "bad_traffic"},
		{name: "pool only for traffic bonus", in: func() PromoBody { x := base; id := int64(4); x.PoolID = &id; return x }(), want: "bad_pool"},
		{name: "minimum without a currency", in: func() PromoBody { x := base; x.MinOrder = 100; return x }(), want: "promo_sum_needs_currency"},
		{name: "maximum without a currency", in: func() PromoBody { x := base; x.MaxDiscount = 5000; return x }(), want: "promo_sum_needs_currency"},
		{name: "new users only unsupported for bonus", in: func() PromoBody { x := base; x.Type = "traffic"; x.Value = 1 << 30; x.NewUsersOnly = true; return x }(), want: "new_users_only_not_supported_for_bonus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePromoBody(tc.in, now, tc.used)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("validatePromoBody() = %v, want %s", err, tc.want)
			}
		})
	}
	ok := base
	ok.Currency, ok.MinOrder, ok.MaxDiscount = "RUB", 100, 500000
	if err := validatePromoBody(ok, now, 0); err != nil {
		t.Fatalf("sums in rubles refused: %v", err)
	}
}
