package gmigrate

import (
	"hash/crc32"
	"testing"
	"testing/fstest"
)

func TestChecksumUsesFlywayLineSemantics(t *testing.T) {
	// Flyway hashes each line without its line ending, so CRLF and LF are equal.
	lf := Checksum([]byte("a\nb\n"))
	crlf := Checksum([]byte("a\r\nb\r\n"))
	want := int32(crc32.ChecksumIEEE([]byte("ab")))
	if lf != want || crlf != want {
		t.Fatalf("checksum = %d/%d, want %d", lf, crlf, want)
	}
	if bom, plain := Checksum([]byte("\uFEFFa\n")), Checksum([]byte("a\n")); bom != plain {
		t.Fatalf("BOM checksum = %d, plain = %d", bom, plain)
	}
}

func TestLoadFSOrdersAndChecksums(t *testing.T) {
	fsys := fstest.MapFS{
		"db/V2__add.sql":  {Data: []byte("ALTER TABLE t ADD c INT;\n")},
		"db/V1__init.sql": {Data: []byte("CREATE TABLE t (id INT);\n")},
	}
	ms, err := LoadFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Version != "1" || ms[1].Version != "2" {
		t.Fatalf("migrations = %+v", ms)
	}
	if ms[0].Script != "db/V1__init.sql" || ms[0].Description != "init" || ms[0].Type != "SQL" {
		t.Fatalf("migration = %+v", ms[0])
	}
	if ms[0].Checksum != Checksum(ms[0].SQL) {
		t.Fatalf("checksum not derived from content")
	}
}

func TestLoadFSRejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"a/V1__one.sql": {Data: []byte("SELECT 1;")},
		"b/V1__two.sql": {Data: []byte("SELECT 2;")},
	}
	if _, err := LoadFS(fsys); err == nil {
		t.Fatal("expected duplicate version error")
	}
}
