package store

import (
	"context"
	"slices"
	"testing"

	"mikan/internal/panel/store/db"
)

// A panel that updates to the node order keeps its nodes where they were (by id), and a
// node added afterwards goes last.
func TestNodeSortMigrationKeepsTheOldOrder(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := postgresProvider(ctx, s.DB, postgresFS)
	if err != nil {
		t.Fatal(err)
	}
	// Roll back the migration under test (and whatever came after it), as a panel from
	// before it would be.
	const migration = 12
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < migration {
			before = src.Version
		}
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	var has bool
	if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='nodes' AND column_name='sort')").Scan(&has); err != nil || has {
		t.Fatalf("nodes.sort after the rollback: %v %v", has, err)
	}
	for _, id := range []int64{9, 1, 4} {
		if _, err := s.DB.ExecContext(ctx, "INSERT INTO nodes(id,name,created_at,updated_at) VALUES($1,'n',1,1) ON CONFLICT(id) DO NOTHING", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, migration); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Q.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids, sorts []int64
	for _, n := range nodes {
		ids, sorts = append(ids, n.ID), append(sorts, n.Sort)
	}
	if !slices.Equal(ids, []int64{1, 4, 9}) || !slices.Equal(sorts, []int64{1, 2, 3}) {
		t.Fatalf("order after the migration: ids %v sorts %v", ids, sorts)
	}

	added, err := s.Q.CreateNode(ctx, db.CreateNodeParams{Name: "new", CreatedAt: 1, UpdatedAt: 1})
	if err != nil || added.Sort != 4 {
		t.Fatalf("a new node goes last: %+v %v", added, err)
	}
	// The last place is the largest sort, whatever the ids say.
	if err := s.Q.SetNodeSort(ctx, db.SetNodeSortParams{Sort: 10, ID: 1}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Q.CreateNode(ctx, db.CreateNodeParams{Name: "newer", CreatedAt: 1, UpdatedAt: 1})
	if err != nil || again.Sort != 11 {
		t.Fatalf("after a reorder: %+v %v", again, err)
	}
	nodes, err = s.Q.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := nodes[len(nodes)-1].ID; got != again.ID {
		t.Fatalf("the newest node is last, got %d", got)
	}
}
