package gmigrate

import (
	"context"
	"testing"
)

func TestRegistrySortsAndRejectsDuplicates(t *testing.T) {
	reg := NewRegistry()
	noop := func(context.Context, Execer) error { return nil }
	if err := reg.Register(GoMigration{Version: "2", Description: "two", Up: noop}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(GoMigration{Version: "1", Description: "one", Up: noop}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(GoMigration{Description: "repeatable", Up: noop}); err != nil {
		t.Fatal(err)
	}
	ms := reg.Migrations()
	if len(ms) != 3 || ms[0].Version != "1" || ms[1].Version != "2" || !ms[2].Repeatable {
		t.Fatalf("migrations = %+v", ms)
	}
	if err := reg.Register(GoMigration{Version: "1", Description: "dup", Up: noop}); err == nil {
		t.Fatal("duplicate version accepted")
	}
	if err := reg.Register(GoMigration{Version: "3", Description: "no-up"}); err == nil {
		t.Fatal("nil Up accepted")
	}
}
