package domain

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// A slot's name is its number, and the nodes keep its counters under it. Slots purged from
// the top of the pool do not give their numbers back: the next ones go on from the last
// number handed out.
func TestRefillDoesNotReuseNamesOfPurgedSlots(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	pool := NewPool(st, func() time.Time { return now })
	names := func() map[string]bool {
		slots, err := st.Q.ListSlots(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, s := range slots {
			m[s.Name] = true
		}
		return m
	}
	if err := pool.Refill(ctx, 5); err != nil {
		t.Fatal(err)
	}
	old := names()
	// Every slot is burned and purged, the top ones included.
	if _, err := st.DB.ExecContext(ctx, "UPDATE slots SET state = 'burned'"); err != nil {
		t.Fatal(err)
	}
	if err := pool.PurgeBurned(ctx); err != nil {
		t.Fatal(err)
	}
	if left := names(); len(left) != 0 {
		t.Fatalf("%d slots survived the purge", len(left))
	}
	if err := pool.Refill(ctx, 3); err != nil {
		t.Fatal(err)
	}
	for name := range names() {
		if old[name] {
			t.Fatalf("the name %s of a purged slot was handed out again", name)
		}
	}
}

// Refills at the same time (an API call and a payment both short of slots) take the
// counter in turns: every name is new, and none is skipped.
func TestConcurrentRefillsGetDistinctNumbers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	pool := NewPool(st, func() time.Time { return now })
	before, err := st.Q.ListSlots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const refills, n = 4, 50
	var wg sync.WaitGroup
	errs := make(chan error, refills)
	for range refills {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- pool.Refill(ctx, n)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	slots, _ := st.Q.ListSlots(ctx)
	if len(slots) != len(before)+refills*n {
		t.Fatalf("%d slots, want %d", len(slots), len(before)+refills*n)
	}
	last, err := st.Q.SlotCounter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range slots {
		seen[s.Name] = true
	}
	for i := last - refills*n + 1; i <= last; i++ {
		if name := fmt.Sprintf("s%06d", i); !seen[name] {
			t.Fatalf("%s is missing: the numbers of the refills overlap", name)
		}
	}
}
