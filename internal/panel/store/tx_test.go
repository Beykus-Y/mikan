package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// A panic inside a transaction (it holds the write lock from BEGIN) rolls it back before
// the panic goes on: the next writer must not wait for a context that never ends.
func TestTxRollsBackOnPanic(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = st.Tx(ctx, func(q *db.Queries) error {
			if err := q.SetSetting(ctx, db.SetSettingParams{Key: "k", Value: "from the panicking transaction"}); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	done := make(chan error, 1)
	go func() {
		done <- st.Tx(ctx, func(q *db.Queries) error {
			return q.SetSetting(ctx, db.SetSettingParams{Key: "other", Value: "v"})
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the write after the panic: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the write lock was not released by the panicking transaction")
	}
	if _, err := st.Q.GetSetting(ctx, "k"); err == nil {
		t.Fatal("what the panicking transaction wrote was committed")
	}
}

func TestTxReturnsFnsError(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	boom := errors.New("boom")
	if err := st.Tx(ctx, func(q *db.Queries) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

// A duplicate is told by the driver's code, not by the words of its message.
func TestIsUnique(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = st.Q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	if !IsUnique(err) {
		t.Fatalf("a duplicate pool name: %v", err)
	}
	if IsUnique(nil) || IsUnique(errors.New("UNIQUE constraint failed: made up")) {
		t.Fatal("something that is not a constraint violation was taken for one")
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO traffic_packages (name, bytes, lifetime, created_at) VALUES ('p', 0, 'used', 1)"); err == nil || IsUnique(err) {
		t.Fatalf("a CHECK violation is not a duplicate: %v", err)
	}
}

// Two writers that each read what the other writes cannot both commit as serializable:
// one is retried, and both end up applied.
func TestTxRetriesASerializationConflict(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, k := range []string{"a", "b"} {
		if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: k, Value: "0"}); err != nil {
			t.Fatal(err)
		}
	}
	before := st.Conflicts()
	// Both first attempts read before either writes: a write skew PostgreSQL must break.
	var read sync.WaitGroup
	read.Add(2)
	errs := make(chan error, 2)
	for _, keys := range [][2]string{{"a", "b"}, {"b", "a"}} {
		var first sync.Once
		go func() {
			errs <- st.Tx(ctx, func(q *db.Queries) error {
				v, err := q.GetSetting(ctx, keys[0])
				if err != nil {
					return err
				}
				first.Do(func() { read.Done(); read.Wait() })
				return q.SetSetting(ctx, db.SetSettingParams{Key: keys[1], Value: v + "+" + keys[0]})
			})
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if st.Conflicts() == before {
		t.Fatal("no conflict was retried")
	}
	a, _ := st.Q.GetSetting(ctx, "a")
	b, _ := st.Q.GetSetting(ctx, "b")
	// Serially: one writer saw the other's value.
	if (a != "0+b" || b != "0+b+a") && (b != "0+a" || a != "0+a+b") {
		t.Fatalf("not a serial outcome: a=%q b=%q", a, b)
	}
}

// TxRC commits like Tx and rolls back when the callback fails.
func TestTxRCCommitsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.TxRC(ctx, func(q *db.Queries) error {
		return q.SetSetting(ctx, db.SetSettingParams{Key: "k", Value: "kept"})
	}); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("failed")
	if err := st.TxRC(ctx, func(q *db.Queries) error {
		if err := q.SetSetting(ctx, db.SetSettingParams{Key: "k", Value: "rolled back"}); err != nil {
			return err
		}
		return failed
	}); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if v, err := st.Q.GetSetting(ctx, "k"); err != nil || v != "kept" {
		t.Fatalf("got %q %v", v, err)
	}
}
