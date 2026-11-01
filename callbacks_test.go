package gmigrate

import (
	"testing"
	"testing/fstest"
)

func TestLoadCallbacksAndSkipInSource(t *testing.T) {
	fsys := fstest.MapFS{
		"beforeMigrate.sql": {Data: []byte("SET search_path = ${schema};")},
		"afterMigrate.sql":  {Data: []byte("ANALYZE;")},
		"V1__init.sql":      {Data: []byte("CREATE TABLE t (id INT);")},
	}
	ms, err := LoadFS(fsys, WithPlaceholders(map[string]string{"schema": "app"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Version != "1" {
		t.Fatalf("callbacks leaked into migrations: %+v", ms)
	}
	callbacks, err := LoadCallbacks(fsys, WithPlaceholders(map[string]string{"schema": "app"}))
	if err != nil {
		t.Fatal(err)
	}
	if string(callbacks.BeforeMigrateSQL) != "SET search_path = app;" {
		t.Fatalf("before callback = %q", callbacks.BeforeMigrateSQL)
	}
	if string(callbacks.AfterMigrateSQL) != "ANALYZE;" {
		t.Fatalf("after callback = %q", callbacks.AfterMigrateSQL)
	}
}
