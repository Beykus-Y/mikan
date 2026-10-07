package domain

import (
	"context"
	"slices"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
)

// The installer bootstraps the panel with its language before the first start, so the
// tariffs are named in it; panels set up without one keep the Russian names.
func TestSeedNamesTariffsInDefaultLang(t *testing.T) {
	for lang, want := range map[string][]string{
		"":   {"Пробный", "Стандарт", "Безлимит"},
		"ru": {"Пробный", "Стандарт", "Безлимит"},
		"en": {"Trial", "Standard", "Unlimited"},
	} {
		ctx := context.Background()
		st, err := storetest.Open(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if lang != "" {
			if err := settings.Set(ctx, settings.New(st.Q), settings.KeyDefaultLang, lang); err != nil {
				t.Fatal(err)
			}
		}
		if err := Seed(ctx, st, time.Now()); err != nil {
			t.Fatal(err)
		}
		tariffs, err := st.Q.ListTariffs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tr := range tariffs {
			got = append(got, tr.Name)
		}
		if !slices.Equal(got, want) {
			t.Errorf("lang %q: tariffs %v, want %v", lang, got, want)
		}
		st.Close()
	}
}
