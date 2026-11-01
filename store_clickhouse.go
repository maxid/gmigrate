package gmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
)

// ClickHouseStore keeps the history in ClickHouse. ClickHouse has no
// transactions or advisory locks: migrations must be idempotent and only one
// migrator may run at a time. Repeatable migrations append a row; the latest
// installed_rank wins.
type ClickHouseStore struct {
	db          *sql.DB
	table       string
	installedBy string
	schema      string
}

func (s *ClickHouseStore) tableName() string {
	if s.schema == "" {
		return s.table
	}
	return s.schema + "." + s.table
}

// OpenClickHouse opens a ClickHouse history store.
func OpenClickHouse(dsn string, opts ...Option) (Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%w: empty ClickHouse DSN", ErrInvalidConfig)
	}
	cfg := newStoreConfig(opts...)
	if !identifierRE.MatchString(cfg.table) {
		return nil, fmt.Errorf("%w: invalid history table %q", ErrInvalidConfig, cfg.table)
	}
	schema := ""
	if len(cfg.schemas) > 0 {
		schema = cfg.schemas[0]
		if !identifierRE.MatchString(schema) {
			return nil, fmt.Errorf("%w: invalid schema %q", ErrInvalidConfig, schema)
		}
	}
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, err
	}
	return &ClickHouseStore{db: db, table: cfg.table, installedBy: cfg.installedBy, schema: schema}, nil
}

func (s *ClickHouseStore) Close() error { return s.db.Close() }

func (s *ClickHouseStore) Ensure(ctx context.Context) error {
	if s.schema != "" {
		if _, err := s.db.ExecContext(ctx, `CREATE DATABASE IF NOT EXISTS `+s.schema); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+s.tableName()+` (
  installed_rank Int32,
  version Nullable(String),
  description String,
  type String,
  script String,
  checksum Nullable(Int32),
  installed_by String,
  installed_on DateTime64(3) DEFAULT now64(3),
  execution_time Int32,
  success Bool
) ENGINE = MergeTree ORDER BY installed_rank`)
	return err
}

func (s *ClickHouseStore) List(ctx context.Context) ([]History, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT installed_rank, version, description, type, script, checksum, installed_by, installed_on, execution_time, success FROM `+s.tableName()+` ORDER BY installed_rank`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []History
	for rows.Next() {
		var h History
		var version sql.NullString
		var checksum sql.NullInt64
		var installedOn time.Time
		if err := rows.Scan(&h.InstalledRank, &version, &h.Description, &h.Type, &h.Script, &checksum, &h.InstalledBy, &installedOn, &h.ExecutionTime, &h.Success); err != nil {
			return nil, err
		}
		h.Version = version.String
		if checksum.Valid {
			v := int32(checksum.Int64)
			h.Checksum = &v
		}
		h.InstalledOn = installedOn.Format(time.RFC3339Nano)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *ClickHouseStore) nextRank(ctx context.Context) (int, error) {
	var rank int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(installed_rank), 0) + 1 FROM `+s.tableName()).Scan(&rank)
	return rank, err
}

func (s *ClickHouseStore) Apply(ctx context.Context, m Migration) error {
	started := time.Now()
	if m.Go != nil {
		if err := m.Go(ctx, s.db); err != nil {
			return fmt.Errorf("%s: %w", m.Script, err)
		}
	} else if _, err := s.db.ExecContext(ctx, string(m.SQL)); err != nil {
		return fmt.Errorf("%s %s: %w", m.Script, m.Version, err)
	}
	rank, err := s.nextRank(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+s.tableName()+` (installed_rank, version, description, type, script, checksum, installed_by, execution_time, success) VALUES (?, ?, ?, ?, ?, ?, ?, ?, TRUE)`,
		rank, nullableString(m.Version), m.Description, m.migrationType(), m.Script, m.Checksum, s.installedBy, int(time.Since(started).Milliseconds()))
	return err
}

func (s *ClickHouseStore) Record(ctx context.Context, m Migration) error {
	rank, err := s.nextRank(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+s.tableName()+` (installed_rank, version, description, type, script, checksum, installed_by, execution_time, success) VALUES (?, ?, ?, ?, ?, NULL, ?, 0, TRUE)`,
		rank, nullableString(m.Version), m.Description, m.migrationType(), m.Script, s.installedBy)
	return err
}

func (s *ClickHouseStore) Exec(ctx context.Context, sql string) error {
	_, err := s.db.ExecContext(ctx, sql)
	return err
}

func (s *ClickHouseStore) DeleteFailed(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `ALTER TABLE `+s.tableName()+` DELETE WHERE success = FALSE`)
	return err
}

func (s *ClickHouseStore) UpdateChecksum(ctx context.Context, script string, checksum int32) error {
	_, err := s.db.ExecContext(ctx, `ALTER TABLE `+s.tableName()+` UPDATE checksum = ? WHERE script = ?`, checksum, script)
	return err
}

func (s *ClickHouseStore) Clean(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+s.tableName())
	return err
}

func (s *ClickHouseStore) Lock(context.Context, string) (func(), error) {
	return func() {}, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
