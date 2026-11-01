package gmigrate

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStoreSchemaQualifiesHistoryAndSetsSearchPath(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, DialectPostgres, WithSchema("tenant_a"))
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("CREATE SCHEMA IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE INDEX IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("SET search_path TO").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE app").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COALESCE").WillReturnRows(sqlmock.NewRows([]string{"rank"}).AddRow(1))
	mock.ExpectExec("INSERT INTO").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.Apply(context.Background(), Migration{Version: "1", Description: "init", Script: "V1__init.sql", Type: "SQL", Checksum: 1, SQL: []byte("CREATE TABLE app (id INT)")}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
