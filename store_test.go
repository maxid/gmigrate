package gmigrate

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func newMockStore(t *testing.T) (*sqlStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	return store.(*sqlStore), mock
}

func TestSQLStoreEnsureAndList(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := sqlmock.NewRows([]string{"installed_rank", "version", "description", "type", "script", "checksum", "installed_by", "installed_on", "execution_time", "success"}).
		AddRow(1, "1", "init", "SQL", "V1__init.sql", 10, "gable", now, 5, true).
		AddRow(2, "5", "baseline", "BASELINE", "<< Flyway Baseline >>", nil, "gable", now, 0, true)
	mock.ExpectQuery("SELECT installed_rank").WillReturnRows(rows)
	history, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Version != "1" || history[0].Checksum == nil || *history[0].Checksum != 10 {
		t.Fatalf("history = %+v", history)
	}
	if history[1].Checksum != nil || history[1].Type != "BASELINE" {
		t.Fatalf("baseline = %+v", history[1])
	}
}

func TestSQLStoreApplyWritesFlywayRow(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE app").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COALESCE").WillReturnRows(sqlmock.NewRows([]string{"rank"}).AddRow(1))
	mock.ExpectExec("INSERT INTO").WithArgs(1, "1", "init", "SQL", "V1__init.sql", int32(10), "gmigrate", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	err := store.Apply(context.Background(), Migration{
		Version: "1", Description: "init", Script: "V1__init.sql", Type: "SQL", Checksum: 10, SQL: []byte("CREATE TABLE app (id INT)"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLStoreRecordWritesNullChecksum(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery("SELECT COALESCE").WillReturnRows(sqlmock.NewRows([]string{"rank"}).AddRow(1))
	mock.ExpectExec("INSERT INTO").WithArgs(1, "5", "<< Flyway Baseline >>", "BASELINE", "<< Flyway Baseline >>", "gmigrate").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.Record(context.Background(), Migration{
		Version: "5", Description: "<< Flyway Baseline >>", Script: "<< Flyway Baseline >>", Type: "BASELINE",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
