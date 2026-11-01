package gmigrate

import (
	"context"
	"errors"
	"testing"
)

type memoryStore struct {
	history []History
	applied []Migration
	failed  int
}

func (s *memoryStore) Ensure(context.Context) error { return nil }
func (s *memoryStore) List(context.Context) ([]History, error) {
	return append([]History(nil), s.history...), nil
}
func (s *memoryStore) Apply(_ context.Context, m Migration) error {
	s.applied = append(s.applied, m)
	checksum := m.Checksum
	if m.Repeatable {
		for i := range s.history {
			if s.history[i].Script == m.Script && s.history[i].Version == "" {
				s.history[i].Checksum = &checksum
				s.history[i].Success = true
				return nil
			}
		}
	}
	s.history = append(s.history, History{InstalledRank: len(s.history) + 1, Version: m.Version, Script: m.Script, Type: m.migrationType(), Checksum: &checksum, Success: true})
	return nil
}
func (s *memoryStore) Record(_ context.Context, m Migration) error {
	s.history = append(s.history, History{InstalledRank: len(s.history) + 1, Version: m.Version, Script: m.Script, Type: m.migrationType(), Success: true})
	return nil
}
func (s *memoryStore) DeleteFailed(context.Context) error {
	var kept []History
	for _, h := range s.history {
		if h.Success {
			kept = append(kept, h)
		} else {
			s.failed++
		}
	}
	s.history = kept
	return nil
}
func (s *memoryStore) UpdateChecksum(_ context.Context, script string, checksum int32) error {
	for i := range s.history {
		if s.history[i].Script == script {
			value := checksum
			s.history[i].Checksum = &value
		}
	}
	return nil
}
func (s *memoryStore) Clean(context.Context) error                  { s.history = nil; return nil }
func (s *memoryStore) Exec(context.Context, string) error           { return nil }
func (s *memoryStore) Lock(context.Context, string) (func(), error) { return func() {}, nil }
func (s *memoryStore) Close() error                                 { return nil }

func migration(version, script string, checksum int32) Migration {
	return Migration{Version: version, Description: "m" + version, Script: script, Type: "SQL", Checksum: checksum}
}

func TestEngineUpIsIdempotent(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	ms := []Migration{migration("1", "V1__init.sql", 10), migration("2", "V2__add.sql", 20)}
	done, err := engine.Up(context.Background(), ms)
	if err != nil || len(done) != 2 {
		t.Fatalf("Up = %d, %v", len(done), err)
	}
	done, err = engine.Up(context.Background(), ms)
	if err != nil || len(done) != 0 {
		t.Fatalf("second Up = %d, %v", len(done), err)
	}
}

func TestEngineRejectsChecksumDrift(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	if _, err := engine.Up(context.Background(), []Migration{migration("1", "V1__init.sql", 10)}); err != nil {
		t.Fatal(err)
	}
	err := engine.Validate(context.Background(), []Migration{migration("1", "V1__init.sql", 11)})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Validate = %v, want ErrValidation", err)
	}
}

func TestEngineBaselineSkipsLowerVersions(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	if err := engine.Baseline(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	done, err := engine.Up(context.Background(), []Migration{
		migration("1", "V1__old.sql", 1),
		migration("6", "V6__new.sql", 6),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Version != "6" {
		t.Fatalf("done = %+v", done)
	}
}

func TestEngineOutOfOrder(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	if _, err := engine.Up(context.Background(), []Migration{migration("2", "V2__two.sql", 2)}); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Up(context.Background(), []Migration{
		migration("2", "V2__two.sql", 2),
		migration("1", "V1__one.sql", 1),
	})
	if !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("Up = %v, want ErrOutOfOrder", err)
	}
}

func TestEngineRepairRemovesFailedRows(t *testing.T) {
	store := &memoryStore{history: []History{{Version: "1", Script: "V1__init.sql", Type: "SQL", Success: false}}}
	engine, _ := NewEngine(store)
	if err := engine.Repair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failed != 1 || len(store.history) != 0 {
		t.Fatalf("failed = %d, history = %+v", store.failed, store.history)
	}
}

func TestEngineBaselineRequiresEmptyHistory(t *testing.T) {
	store := &memoryStore{history: []History{{Version: "1", Type: "SQL", Success: true}}}
	engine, _ := NewEngine(store)
	if err := engine.Baseline(context.Background(), "1"); !errors.Is(err, ErrBaselineExists) {
		t.Fatalf("Baseline = %v, want ErrBaselineExists", err)
	}
}

func repeatable(script string, checksum int32) Migration {
	return Migration{Description: "r", Script: script, Type: "SQL", Repeatable: true, Checksum: checksum}
}

func TestRepeatableRunsWhenChecksumChanges(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	ms := []Migration{migration("1", "V1__init.sql", 1), repeatable("R__view.sql", 10)}
	if _, err := engine.Up(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Up(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if len(store.applied) != 2 {
		t.Fatalf("repeatable re-ran without a checksum change: %+v", store.applied)
	}
	ms[1].Checksum = 11
	if done, err := engine.Up(context.Background(), ms); err != nil || len(done) != 1 || !done[0].Repeatable {
		t.Fatalf("changed repeatable = %+v, %v", done, err)
	}
}

func TestInfoStates(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	ms := []Migration{migration("1", "V1__init.sql", 1), migration("2", "V2__next.sql", 2)}
	if _, err := engine.Up(context.Background(), []Migration{ms[0]}); err != nil {
		t.Fatal(err)
	}
	info, err := engine.Info(context.Background(), ms)
	if err != nil {
		t.Fatal(err)
	}
	if len(info) != 2 || info[0].State != "Success" || info[1].State != "Pending" {
		t.Fatalf("info = %+v", info)
	}
}

func TestBaselineOnMigrate(t *testing.T) {
	store := &memoryStore{}
	engine, _ := New(store, Config{BaselineOnMigrate: true, BaselineVersion: "5"})
	done, err := engine.Up(context.Background(), []Migration{migration("1", "V1__old.sql", 1), migration("6", "V6__new.sql", 6)})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Version != "6" {
		t.Fatalf("done = %+v", done)
	}
}

func TestRepairRealignsChecksum(t *testing.T) {
	store := &memoryStore{}
	engine, _ := NewEngine(store)
	ms := []Migration{migration("1", "V1__init.sql", 1)}
	if _, err := engine.Up(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	ms[0].Checksum = 2
	if err := engine.Repair(context.Background(), ms[0]); err != nil {
		t.Fatal(err)
	}
	if store.history[0].Checksum == nil || *store.history[0].Checksum != 2 {
		t.Fatalf("checksum = %+v", store.history[0].Checksum)
	}
	if err := engine.Validate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
}

func TestCleanDisabled(t *testing.T) {
	engine, _ := NewEngine(&memoryStore{}, WithCleanDisabled(true))
	if err := engine.Clean(context.Background()); !errors.Is(err, ErrCleanDisabled) {
		t.Fatalf("Clean = %v", err)
	}
}
