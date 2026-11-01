[English](README.md) | 简体中文

# gmigrate

Go 的数据库版本管理，对齐 Flyway Core 工作流：versioned/repeatable 迁移、Flyway 兼容历史表、validate/baseline/repair/info/clean、placeholders、callbacks 与独立 CLI。

> 状态：**实验性脚手架**（v0.0.0-dev），API 尚不稳定。
> 范围：SQL + 注册式 Go 迁移；MySQL、PostgreSQL、ClickHouse、TDengine。

## 为什么

- **Flyway 兼容历史**：默认 `flyway_schema_history` 列与 Flyway 的行级 CRC32 checksum，已有 Flyway 库可直接接管，不需要复制历史行。
- **SQL + Go**：`V*.sql` / `R*.sql` 做 schema 变更；需要应用逻辑时用编译期 Go 迁移。
- **Core 工作流**：`migrate`、`info`、`validate`、`baseline`、`repair`、`clean`、out-of-order、ignore-missing。
- **独立**：不依赖 gable/gns 或其它框架；CLI 可单独运行，应用也可直接用库。

## 迁移文件

```text
db/migrations/
  V1__init.sql
  V2__add_task_index.sql
  R__task_view.sql
  U2__add_task_index.sql
```

- `V<version>__<description>.sql`：versioned，按版本顺序只跑一次。
- `R__<description>.sql`：repeatable，在 versioned 之后执行，checksum 变化才重跑。
- `U<version>__<description>.sql`：versioned 的 undo；`gmigrate undo` 执行后删除原历史行。Go 迁移用可选的 `Down` 函数。
- Placeholder 使用 `${key}`，在 checksum 计算**之后**、执行之前替换，因此改 placeholder 值不会改变历史 checksum。

## 库

```go
store, _ := gmigrate.OpenPostgres(dsn)
defer store.Close()

ms, _ := gmigrate.LoadDir("db/migrations",
    gmigrate.WithPlaceholders(map[string]string{"schema": "app"}))
ms = append(ms, gmigrate.Registered()...) // Go migrations
ms = gmigrate.SortMigrations(ms)

engine, _ := gmigrate.New(store, gmigrate.Config{
    OutOfOrder:       false,
    BaselineOnMigrate: true,
    BaselineVersion:  "20261031",
})
applied, err := engine.Migrate(ctx, ms)
```

Go 迁移在编译期注册：

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

其它操作：

```go
err = engine.Validate(ctx, ms)
rows, _ := engine.Info(ctx, ms)
err = engine.Baseline(ctx, "20261031")
err = engine.Repair(ctx, ms...)
err = engine.Clean(ctx) // CLI 默认禁用
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
gmigrate -url 'postgres://...' -locations db/migrations undo              # 最新一个
gmigrate -url 'postgres://...' -locations db/migrations -target 2.0 undo  # 撤销 >2.0
gmigrate -url 'postgres://...' -clean-disabled=false clean
gmigrate -url 'postgres://...' -locations db/migrations -dry-run migrate
```

支持 `-placeholder key=value`（可重复）、`-table`、`-schema`、`-installed-by`、`-out-of-order`、`-ignore-missing`、`-baseline-on-migrate`、`-cherry-pick <version>`。

`-schema` 指定一个默认 schema；`-schemas a,b,c` 设置有序列表（第一个是默认）。PostgreSQL 用完整 `search_path` 执行迁移；MySQL 用第一个 database 执行 `USE`。历史表位于第一个 schema。

CLI 的 Go 迁移来自链接进二进制的 `gmigrate.Register`；产品 CLI 可 blank-import 自己的迁移包。

## 数据库

| 数据库 | Store | 事务/锁 |
|---|---|---|
| MySQL/MariaDB/TiDB | `OpenMySQL` | 每迁移事务；`GET_LOCK` |
| PostgreSQL/Kingbase/GaussDB | `OpenPostgres` | 每迁移事务；advisory lock |
| ClickHouse | `OpenClickHouse` | 无事务/锁；历史 append-only，repair 用 mutation |
| TDengine | `OpenTDengine` | REST；无事务/锁；不支持 Go 迁移 |

MySQL 使用 `parseTime=true`；多语句文件加 `multiStatements=true`。

## 兼容与限制

- 历史列：`installed_rank`、`version`、`description`、`type`、`script`、`checksum`、`installed_by`、`installed_on`、`execution_time`、`success`。
- Checksum：Flyway 的行级 CRC32。
- 已支持有序 schema 列表（PostgreSQL 全量 `search_path`；MySQL/ClickHouse/TDengine 使用第一个）。clean 会清理所有配置 schema。Undo、dry-run 与 cherry-pick（`-cherry-pick`）已实现。
- ClickHouse/TDengine 在引擎不能更新行时使用 append-only 历史；repeatable 以最大 `installed_rank` 为准。

## 开发

```bash
go test ./...
go build ./cmd/gmigrate

# 通过 testcontainers 跑真实数据库（需要 Docker）
go test -tags=integration ./...
```

集成测试使用 `mysql:8.4.7`、`postgres:15-alpine`、
`clickhouse/clickhouse-server:24.8-alpine`、`tdengine/tsdb:3.4.0.9`。

MIT，见 [LICENSE](LICENSE)。
