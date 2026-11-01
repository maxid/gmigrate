package gmigrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadFSRepeatableAndPlaceholders(t *testing.T) {
	fsys := fstest.MapFS{
		"db/V1__init.sql": {Data: []byte("CREATE TABLE ${schema}.t (id INT);")},
		"db/R__view.sql":  {Data: []byte("CREATE VIEW ${schema}.v AS SELECT 1;")},
	}
	ms, err := LoadFS(fsys, WithPlaceholders(map[string]string{"schema": "app"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Version != "1" || !ms[1].Repeatable {
		t.Fatalf("migrations = %+v", ms)
	}
	if !strings.Contains(string(ms[0].SQL), "app.t") || !strings.Contains(string(ms[1].SQL), "app.v") {
		t.Fatalf("placeholders not applied: %q / %q", ms[0].SQL, ms[1].SQL)
	}
	if ms[0].Checksum == Checksum(ms[0].SQL) {
		t.Fatal("checksum must cover the raw script, not the substituted SQL")
	}
}

func TestLoadFSRejectsMissingPlaceholder(t *testing.T) {
	fsys := fstest.MapFS{"db/V1__x.sql": {Data: []byte("SELECT ${missing};")}}
	if _, err := LoadFS(fsys, WithPlaceholders(nil)); err == nil {
		t.Fatal("missing placeholder accepted")
	}
}
