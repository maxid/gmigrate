// Package migrate applies versioned SQL migrations and keeps a Flyway-compatible
// history table.
//
// The history table is deliberately the Flyway schema (flyway_schema_history),
// not a private ledger: an existing Flyway-managed database can be adopted
// without copying rows, and Flyway can still validate the same history. A
// migration is only recorded after its SQL succeeded; a failed row is repaired
// explicitly with Engine.Repair.
package gmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Framework errors.
var (
	ErrInvalidConfig  = errors.New("migrate: invalid config")
	ErrValidation     = errors.New("migrate: validation failed")
	ErrOutOfOrder     = errors.New("migrate: out-of-order migration")
	ErrBaselineExists = errors.New("migrate: history is not empty")
	ErrCleanDisabled  = errors.New("migrate: clean is disabled")
	ErrUnsupported    = errors.New("migrate: unsupported operation")
)

// Execer is the execution surface handed to Go migrations. It is satisfied by
// *sql.Tx and *sql.DB.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// GoFunc is the body of a programmatic migration.
type GoFunc func(ctx context.Context, tx Execer) error

// Migration is one versioned SQL file.
type Migration struct {
	// Version is the dotted numeric version parsed from the file name.
	Version string
	// Description is the file name text after "__".
	Description string
	// Script is the path relative to the migration root. It is stored in
	// flyway_schema_history.script.
	Script string
	// Type is "SQL" for a normal migration and "BASELINE" for a baseline row.
	Type string
	// Checksum is Flyway's CRC32 checksum (int32, may be negative).
	Checksum int32
	// SQL is the file content after placeholder replacement.
	SQL []byte
	// Go is set for a programmatic migration; it runs instead of SQL.
	Go GoFunc
	// Repeatable migrations run again when their checksum changes.
	Repeatable bool
	// Undo marks a U<version> migration used by Engine.Undo.
	Undo bool
}

// History is one row of flyway_schema_history.
type History struct {
	InstalledRank int
	Version       string
	Description   string
	Type          string
	Script        string
	// Checksum is nil for a baseline row.
	Checksum      *int32
	InstalledBy   string
	InstalledOn   string
	ExecutionTime int
	Success       bool
}

// Status summarizes the applied/pending state.
type Status struct {
	Applied  int
	Pending  int
	Failed   int
	Baseline string
	Latest   string
}

func (m Migration) migrationType() string {
	if m.Type != "" {
		return m.Type
	}
	if m.Undo {
		return "UNDO_SQL"
	}
	if m.Go != nil {
		return "GO"
	}
	return "SQL"
}

// Key identifies a migration in the source and history. Versioned migrations
// use their version; repeatable migrations use their script path.
func (m Migration) Key() string {
	if m.Undo {
		return "U:" + m.Version
	}
	if m.Repeatable || m.Version == "" {
		return "R:" + m.Script
	}
	return "V:" + m.Version
}

func (h History) Key() string {
	if h.Version == "" {
		return "R:" + h.Script
	}
	return "V:" + h.Version
}

// VersionCompare compares dotted numeric versions. Missing segments count as
// zero, so "1.2" equals "1.2.0".
func VersionCompare(a, b string) int {
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv uint64
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func splitVersion(v string) []uint64 {
	var out []uint64
	for _, seg := range splitDots(v) {
		var n uint64
		for _, c := range seg {
			if c < '0' || c > '9' {
				return out
			}
			n = n*10 + uint64(c-'0')
		}
		out = append(out, n)
	}
	return out
}

func splitDots(v string) []string {
	var out []string
	start := 0
	for i := 0; i < len(v); i++ {
		if v[i] == '.' {
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}

func validateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("%w: empty version", ErrInvalidConfig)
	}
	for _, seg := range splitDots(v) {
		if seg == "" {
			return fmt.Errorf("%w: invalid version %q", ErrInvalidConfig, v)
		}
		for _, c := range seg {
			if c < '0' || c > '9' {
				return fmt.Errorf("%w: invalid version %q", ErrInvalidConfig, v)
			}
		}
	}
	return nil
}
