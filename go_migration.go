package gmigrate

import (
	"fmt"
	"hash/crc32"
	"reflect"
	"runtime"
	"sort"
	"sync"
)

// GoMigration is a programmatic migration registered at build time. Version is
// empty for a repeatable migration; Description is required. If Checksum is
// nil, the checksum is derived from version, description and the Up function
// name, so replacing the function body without changing its name must supply an
// explicit Checksum.
type GoMigration struct {
	Version     string
	Description string
	Up          GoFunc
	Down        GoFunc
	Checksum    *int32
}

// Registry collects Go migrations. The package-level Default registry is used
// by the CLI; applications may use their own Registry instead.
type Registry struct {
	mu    sync.Mutex
	byKey map[string]Migration
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byKey: map[string]Migration{}} }

// Default is the process-wide registry used by Register and Registered.
var Default = NewRegistry()

// Register adds a migration to the default registry.
func Register(m GoMigration) error { return Default.Register(m) }

// MustRegister is Register for init functions; it panics on a configuration
// error so a bad registration fails at startup.
func MustRegister(m GoMigration) {
	if err := Register(m); err != nil {
		panic(err)
	}
}

// Register validates and adds a migration. Versioned migrations need a numeric
// version; repeatable migrations leave Version empty and must be unique by
// description.
func (r *Registry) Register(m GoMigration) error {
	if m.Up == nil {
		return fmt.Errorf("%w: Go migration %q has no Up function", ErrInvalidConfig, m.Description)
	}
	if m.Description == "" {
		return fmt.Errorf("%w: Go migration needs a description", ErrInvalidConfig)
	}
	if m.Version != "" {
		if err := validateVersion(m.Version); err != nil {
			return err
		}
	}
	checksum := int32(0)
	if m.Checksum != nil {
		checksum = *m.Checksum
	} else {
		name := runtime.FuncForPC(reflect.ValueOf(m.Up).Pointer()).Name()
		checksum = int32(crc32.ChecksumIEEE([]byte(m.Version + "\x00" + m.Description + "\x00" + name)))
	}
	migration := Migration{
		Version:     m.Version,
		Description: m.Description,
		Script:      "go:" + m.Description,
		Type:        "GO",
		Checksum:    checksum,
		Go:          m.Up,
		Repeatable:  m.Version == "",
	}
	key := migration.Key()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byKey[key]; exists {
		return fmt.Errorf("%w: duplicate Go migration %s", ErrInvalidConfig, key)
	}
	r.byKey[key] = migration
	r.order = append(r.order, key)
	if m.Down != nil {
		downChecksum := int32(crc32.ChecksumIEEE([]byte(m.Version + "\x00undo\x00" + runtime.FuncForPC(reflect.ValueOf(m.Down).Pointer()).Name())))
		undo := Migration{
			Version:     m.Version,
			Description: m.Description,
			Script:      "go:undo:" + m.Description,
			Type:        "UNDO_SQL",
			Checksum:    downChecksum,
			Go:          m.Down,
			Undo:        true,
		}
		r.byKey[undo.Key()] = undo
		r.order = append(r.order, undo.Key())
	}
	return nil
}

// Migrations returns a sorted copy: versioned by version, then repeatable by
// description.
func (r *Registry) Migrations() []Migration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Migration, 0, len(r.order))
	for _, key := range r.order {
		out = append(out, r.byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Repeatable != out[j].Repeatable {
			return !out[i].Repeatable
		}
		if out[i].Repeatable {
			return out[i].Script < out[j].Script
		}
		if c := VersionCompare(out[i].Version, out[j].Version); c != 0 {
			return c < 0
		}
		return out[i].Script < out[j].Script
	})
	return out
}

// Registered returns the default registry's migrations.
func Registered() []Migration { return Default.Migrations() }
