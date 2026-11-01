English | [简体中文](README.zh-CN.md)

# gmigrate

Database version management for Go, aligned with the Flyway Core workflow: versioned and repeatable migrations, a Flyway-compatible history table, validation, baseline, repair, info, clean, placeholders, callbacks and an independent CLI.

> Status: **experimental scaffold** (v0.0.0-dev). The API is not stable yet.
> Scope: SQL + registered Go migrations; MySQL, PostgreSQL, ClickHouse and TDengine.

## Why

- **Flyway-compatible history**: the default `flyway_schema_history` columns and Flyway's line-based CRC32 checksum let an existing Flyway-managed database be adopted without copying rows.
- **SQL and Go**: plain `V*.sql` / `R*.sql` files for schema changes; compiled Go migrations when a change needs application logic.
- **Core workflow**: `migrate`, `info`, `validate`, `baseline`, `repair`, `clean`, out-of-order and ignore-missing.
- **Independent**: no dependency on gable, gns or another framework. The CLI runs on its own; applications can use the library directly.

## Migration files

```text
db/migrations/
  V1__init.sql
  V2__add_task_index.sql
  R__task_view.sql
```

- `V<version>__<description>.sql`: versioned; runs once, in version order.
- `R__<description>.sql`: repeatable; runs after versioned migrations, and again only when its checksum changes.
- Placeholders use `${key}` and are replaced **after** the checksum is calculated, so changing a placeholder value does not change history checksums.

## Library

```go
store, _ := gmigrate.OpenPostgres(dsn)
defer store.Close()

ms, _ := gmigrate.LoadDir("db/migrations",
    gmigrate.WithPlaceholders(map[string]string{"schema": "app"}))
ms = append(ms, gmigrate.Registered()...) // Go migrations
ms = gmigrate.SortMigrations(ms)

engine, _ := gmigrate.New(store, gmigrate.Config{
    OutOfOrder:     false,
    BaselineOnMigrate: true,
    BaselineVersion:  "20261031",
})
applied, err := engine.Migrate(ctx, ms)
```

Go migrations are registered at build time:

```go
func init() {
    gmigrate.MustRegister(gmigrate.GoMigration{
        Version:     "20261101",
        Description: "backfill task status",
        Up: func(ctx context.Context, tx gmigrate.Execer) error {
            _, err := tx.ExecContext(ctx, "UPDATE task SET status = 'open' WHERE status = ''")
            return err
        },
    })
}
```

Other operations:

```go
err = engine.Validate(ctx, ms)
rows, _ := engine.Info(ctx, ms)
err = engine.Baseline(ctx, "20261031")
err = engine.Repair(ctx, ms...)
err = engine.Clean(ctx) // clean-disabled by default in the CLI
```

## CLI

```bash
gmigrate -url 'postgres://user:pass@127.0.0.1:5432/app?sslmode=disable' \
  -locations db/migrations migrate

gmigrate -url 'mysql://user:pass@tcp(127.0.0.1:3306)/app?parseTime=true' \
  -locations db/migrations info

gmigrate -url 'postgres://...' -locations db/migrations validate
gmigrate -url 'postgres://...' -baseline-version 20261031 baseline
gmigrate -url 'postgres://...' -locations db/migrations repair
gmigrate -url 'postgres://...' -clean-disabled=false clean
```

Use `-placeholder key=value` (repeatable), `-table`, `-schema`, `-installed-by`, `-out-of-order`, `-ignore-missing`, `-baseline-on-migrate`.

`-schema` selects the default schema/database. PostgreSQL runs migrations with `search_path` set to it; MySQL uses `USE`. The history table is qualified with the same schema.

The CLI's Go migrations come from `gmigrate.Register` in packages linked into the binary; a product CLI can blank-import its migration package.

## Databases

| Database | Store | Transactions / lock |
|---|---|---|
| MySQL/MariaDB/TiDB | `OpenMySQL` | per-migration transaction; `GET_LOCK` |
| PostgreSQL/Kingbase/GaussDB | `OpenPostgres` | per-migration transaction; advisory lock |
| ClickHouse | `OpenClickHouse` | no transaction/lock; append-only history, mutations for repair |
| TDengine | `OpenTDengine` | REST; no transaction/lock; Go migrations unsupported |

For MySQL use `parseTime=true` and `multiStatements=true` when a migration file has multiple statements.

## Compatibility and limits

- History columns: `installed_rank`, `version`, `description`, `type`, `script`, `checksum`, `installed_by`, `installed_on`, `execution_time`, `success`.
- Checksum: Flyway's CRC32 over lines without line endings.
- Default schema/search_path is supported. A full ordered multi-schema list, Undo, cherry-pick and dry-run are not yet implemented.
- ClickHouse/TDengine history is append-only where the engine cannot update rows; the latest `installed_rank` wins for repeatable migrations.

## Development

```bash
go test ./...
go build ./cmd/gmigrate
```

MIT. See [LICENSE](LICENSE).
