//go:build integration

package gmigrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/maxid/gmigrate"
)

func startContainer(t *testing.T, image string, port string, env map[string]string) (string, int) {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{port},
		Env:          env,
		WaitingFor:   wait.ForListeningPort(port).WithStartupTimeout(3 * time.Minute),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start %s: %v", image, err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := container.MappedPort(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	return host, int(mapped.Num())
}

func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func waitPing(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.Ping(); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("database did not become ready")
}

func TestMySQLEndToEnd(t *testing.T) {
	host, port := startContainer(t, "mysql:8.4.7", "3306/tcp", map[string]string{
		"MYSQL_ROOT_PASSWORD": "root",
		"MYSQL_DATABASE":      "gmigrate",
	})
	dsn := fmt.Sprintf("root:root@tcp(%s:%d)/gmigrate?parseTime=true&multiStatements=true", host, port)
	store, err := gmigrate.OpenMySQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	waitPing(t, db)

	dir := writeMigrations(t, map[string]string{
		"V1__init.sql":   "CREATE TABLE gm_task (id INT PRIMARY KEY, title VARCHAR(64));",
		"V2__seed.sql":   "INSERT INTO gm_task (id, title) VALUES (1, 'first');",
		"U2__seed.sql":   "DELETE FROM gm_task WHERE id = 1;",
		"R__visible.sql": "CREATE OR REPLACE VIEW gm_visible AS SELECT id, title FROM gm_task;",
	})
	ms, err := gmigrate.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := gmigrate.New(store, gmigrate.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Migrate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := db.QueryRow("SELECT title FROM gm_visible WHERE id = 1").Scan(&title); err != nil || title != "first" {
		t.Fatalf("view = %q, %v", title, err)
	}
	if _, err := engine.Undo(context.Background(), ms, "1"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM gm_task").Scan(&count); err != nil || count != 0 {
		t.Fatalf("undo count = %d, %v", count, err)
	}
}

func TestPostgresEndToEndWithSchema(t *testing.T) {
	host, port := startContainer(t, "postgres:15-alpine", "5432/tcp", map[string]string{
		"POSTGRES_PASSWORD": "postgres",
		"POSTGRES_DB":       "gmigrate",
	})
	dsn := fmt.Sprintf("postgres://postgres:postgres@%s:%d/gmigrate?sslmode=disable", host, port)
	store, err := gmigrate.OpenPostgres(dsn, gmigrate.WithSchemas("tenant_a", "shared"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	waitPing(t, db)

	dir := writeMigrations(t, map[string]string{
		"V1__init.sql": "CREATE TABLE gm_task (id INT PRIMARY KEY, title TEXT);",
		"V2__seed.sql": "INSERT INTO gm_task (id, title) VALUES (1, 'first');",
		"U2__seed.sql": "DELETE FROM gm_task WHERE id = 1;",
	})
	ms, err := gmigrate.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := gmigrate.New(store, gmigrate.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Migrate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := db.QueryRow("SELECT title FROM tenant_a.gm_task WHERE id = 1").Scan(&title); err != nil || title != "first" {
		t.Fatalf("row = %q, %v", title, err)
	}
	if _, err := engine.Undo(context.Background(), ms, "1"); err != nil {
		t.Fatal(err)
	}
}

func tdExec(ctx context.Context, base, statement string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/rest/sql", strings.NewReader(statement))
	if err != nil {
		return err
	}
	req.SetBasicAuth("root", "taosdata")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("tdengine: %s: %s", resp.Status, body)
	}
	return nil
}

func TestTDengineEndToEnd(t *testing.T) {
	host, port := startContainer(t, "tdengine/tsdb:3.4.0.9", "6041/tcp", nil)
	base := fmt.Sprintf("http://%s:%d", host, port)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := tdExec(context.Background(), base, "CREATE DATABASE IF NOT EXISTS gmigrate"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("TDengine REST did not become ready")
		}
		time.Sleep(500 * time.Millisecond)
	}
	store, err := gmigrate.OpenTDengine(strings.Replace(base, "http://", "http://root:taosdata@", 1) + "/gmigrate")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := writeMigrations(t, map[string]string{
		"V1__init.sql": "CREATE TABLE gm_task (ts TIMESTAMP, id INT, title NCHAR(64));",
		"V2__seed.sql": "INSERT INTO gm_task (ts, id, title) VALUES (NOW, 1, 'first');",
	})
	ms, err := gmigrate.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := gmigrate.New(store, gmigrate.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Migrate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	rows, err := engine.Info(context.Background(), ms)
	if err != nil || len(rows) != 2 || rows[0].State != "Success" {
		t.Fatalf("info = %+v, %v", rows, err)
	}
}

func TestClickHouseEndToEnd(t *testing.T) {
	host, port := startContainer(t, "clickhouse/clickhouse-server:24.8-alpine", "9000/tcp", map[string]string{
		"CLICKHOUSE_USER":     "default",
		"CLICKHOUSE_PASSWORD": "clickhouse",
	})
	dsn := fmt.Sprintf("clickhouse://default:clickhouse@%s:%d?database=default", host, port)
	store, err := gmigrate.OpenClickHouse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := writeMigrations(t, map[string]string{
		"V1__init.sql": "CREATE TABLE gm_task (id Int32, title String) ENGINE = MergeTree ORDER BY id;",
		"V2__seed.sql": "INSERT INTO gm_task (id, title) VALUES (1, 'first');",
	})
	ms, err := gmigrate.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := gmigrate.New(store, gmigrate.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Migrate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	rows, err := engine.Info(context.Background(), ms)
	if err != nil || len(rows) != 2 || rows[0].State != "Success" {
		t.Fatalf("info = %+v, %v", rows, err)
	}
}
