package gmigrate

import (
	"context"
	"fmt"
	"sort"
)

const lockName = "gmigrate"

// Callbacks are optional hooks around migrations. SQL callback files are
// loaded by the CLI for the corresponding phases; the fields here are the Go
// equivalent.
type Callbacks struct {
	BeforeMigrate     func(context.Context) error
	BeforeMigrateSQL  []byte
	AfterMigrate      func(context.Context) error
	AfterMigrateSQL   []byte
	BeforeEach        func(context.Context, Migration) error
	BeforeEachSQL     []byte
	AfterEach         func(context.Context, Migration) error
	AfterEachSQL      []byte
	BeforeValidate    func(context.Context) error
	BeforeValidateSQL []byte
	AfterValidate     func(context.Context) error
	AfterValidateSQL  []byte
	BeforeBaseline    func(context.Context) error
	BeforeBaselineSQL []byte
	AfterBaseline     func(context.Context) error
	AfterBaselineSQL  []byte
	BeforeRepair      func(context.Context) error
	BeforeRepairSQL   []byte
	AfterRepair       func(context.Context) error
	AfterRepairSQL    []byte
	BeforeClean       func(context.Context) error
	BeforeCleanSQL    []byte
	AfterClean        func(context.Context) error
	AfterCleanSQL     []byte
	BeforeUndo        func(context.Context) error
	BeforeUndoSQL     []byte
	AfterUndo         func(context.Context) error
	AfterUndoSQL      []byte
}

// Config configures an Engine.
type Config struct {
	OutOfOrder        bool
	IgnoreMissing     bool
	BaselineOnMigrate bool
	BaselineVersion   string
	CleanDisabled     bool
	CherryPick        string
	Callbacks         Callbacks
}

// Engine validates and applies migrations against a Store.
type Engine struct {
	store             Store
	outOfOrder        bool
	ignoreMissing     bool
	baselineOnMigrate bool
	baselineVersion   string
	cleanDisabled     bool
	cherryPick        string
	callbacks         Callbacks
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithOutOfOrder allows a lower pending version to run after a higher applied
// version.
func WithOutOfOrder(allow bool) EngineOption {
	return func(e *Engine) { e.outOfOrder = allow }
}

// WithIgnoreMissing ignores applied migrations whose source file disappeared.
func WithIgnoreMissing(ignore bool) EngineOption {
	return func(e *Engine) { e.ignoreMissing = ignore }
}

// WithBaselineOnMigrate records BaselineVersion when the history is empty.
func WithBaselineOnMigrate(version string) EngineOption {
	return func(e *Engine) {
		if version != "" {
			e.baselineOnMigrate = true
			e.baselineVersion = version
		}
	}
}

// WithCleanDisabled prevents Engine.Clean from running.
func WithCleanDisabled(disabled bool) EngineOption {
	return func(e *Engine) { e.cleanDisabled = disabled }
}

// WithCallbacks installs lifecycle hooks.
func WithCallbacks(callbacks Callbacks) EngineOption {
	return func(e *Engine) { e.callbacks = callbacks }
}

// WithCherryPick applies only the named pending version.
func WithCherryPick(version string) EngineOption {
	return func(e *Engine) { e.cherryPick = version }
}

// NewEngine creates an engine backed by store.
func NewEngine(store Store, opts ...EngineOption) (*Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalidConfig)
	}
	e := &Engine{store: store}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// New creates an engine from Config.
func New(store Store, cfg Config) (*Engine, error) {
	e, err := NewEngine(store,
		WithOutOfOrder(cfg.OutOfOrder),
		WithIgnoreMissing(cfg.IgnoreMissing),
		WithCleanDisabled(cfg.CleanDisabled),
		WithCallbacks(cfg.Callbacks),
		WithCherryPick(cfg.CherryPick),
	)
	if err != nil {
		return nil, err
	}
	if cfg.BaselineOnMigrate && cfg.BaselineVersion != "" {
		e.baselineOnMigrate = true
		e.baselineVersion = cfg.BaselineVersion
	}
	return e, nil
}

// Migrate is the Flyway-style name for Up.
func (e *Engine) Migrate(ctx context.Context, ms []Migration) ([]Migration, error) {
	return e.Up(ctx, ms)
}

// Validate checks a source set against the history without changing it.
func (e *Engine) Validate(ctx context.Context, ms []Migration) error {
	if e.callbacks.BeforeValidateSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.BeforeValidateSQL)); err != nil {
			return err
		}
	}
	if e.callbacks.BeforeValidate != nil {
		if err := e.callbacks.BeforeValidate(ctx); err != nil {
			return err
		}
	}
	if err := e.store.Ensure(ctx); err != nil {
		return err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return err
	}
	if err := e.validate(history, forwardOnly(ms)); err != nil {
		return err
	}
	if e.callbacks.AfterValidate != nil {
		return e.callbacks.AfterValidate(ctx)
	}
	if e.callbacks.AfterValidateSQL != nil {
		return e.store.Exec(ctx, string(e.callbacks.AfterValidateSQL))
	}
	return nil
}

// Info returns one row per source or history migration, Flyway-style.
type Info struct {
	Version       string
	Description   string
	Script        string
	Type          string
	State         string
	Applied       bool
	Repeatable    bool
	Checksum      *int32
	InstalledOn   string
	ExecutionTime int
}

// Info reports applied, pending, missing and failed migrations.
func (e *Engine) Info(ctx context.Context, ms []Migration) ([]Info, error) {
	if err := e.store.Ensure(ctx); err != nil {
		return nil, err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return nil, err
	}
	applied := map[string]History{}
	for _, h := range history {
		applied[h.Key()] = h
	}
	var out []Info
	for _, m := range forwardOnly(ms) {
		info := Info{Version: m.Version, Description: m.Description, Script: m.Script, Type: m.migrationType(), Repeatable: m.Repeatable}
		if h, ok := applied[m.Key()]; ok {
			info.Applied = true
			info.Checksum = h.Checksum
			info.InstalledOn = h.InstalledOn
			info.ExecutionTime = h.ExecutionTime
			switch {
			case h.Type == "BASELINE":
				info.State = "Baseline"
			case !h.Success:
				info.State = "Failed"
			case m.Repeatable && (h.Checksum == nil || *h.Checksum != m.Checksum):
				info.State = "Pending"
			default:
				info.State = "Success"
			}
		} else {
			info.State = "Pending"
		}
		out = append(out, info)
	}
	for _, h := range history {
		if _, ok := sourceByKey(ms)[h.Key()]; ok {
			continue
		}
		info := Info{Version: h.Version, Description: h.Description, Script: h.Script, Type: h.Type, Applied: h.Success, Checksum: h.Checksum, InstalledOn: h.InstalledOn, ExecutionTime: h.ExecutionTime}
		switch {
		case h.Type == "BASELINE":
			info.State = "Baseline"
		case !h.Success:
			info.State = "Failed"
		default:
			info.State = "Missing"
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Repeatable != out[j].Repeatable {
			return !out[i].Repeatable
		}
		if out[i].Repeatable {
			return out[i].Script < out[j].Script
		}
		return VersionCompare(out[i].Version, out[j].Version) < 0
	})
	return out, nil
}

// Status reports applied, pending and failed counts.
func (e *Engine) Status(ctx context.Context, ms []Migration) (Status, error) {
	if err := e.store.Ensure(ctx); err != nil {
		return Status{}, err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{}
	applied := map[string]History{}
	for _, h := range history {
		if !h.Success {
			status.Failed++
			continue
		}
		if h.Type == "BASELINE" {
			status.Baseline = h.Version
			continue
		}
		applied[h.Key()] = h
		if h.Version != "" {
			status.Applied++
		}
	}
	for _, m := range forwardOnly(ms) {
		if e.cherryPick != "" && m.Version != e.cherryPick {
			continue
		}
		if h, ok := applied[m.Key()]; ok {
			if !m.Repeatable || (h.Checksum != nil && *h.Checksum == m.Checksum) {
				continue
			}
		}
		if status.Baseline != "" && m.Version != "" && VersionCompare(m.Version, status.Baseline) <= 0 {
			continue
		}
		status.Pending++
		if m.Version != "" && (status.Latest == "" || VersionCompare(m.Version, status.Latest) > 0) {
			status.Latest = m.Version
		}
	}
	return status, nil
}

// Up applies every pending migration in order and returns the applied ones.
func (e *Engine) Up(ctx context.Context, ms []Migration) ([]Migration, error) {
	release, err := e.store.Lock(ctx, lockName)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := e.store.Ensure(ctx); err != nil {
		return nil, err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return nil, err
	}
	if e.baselineOnMigrate && len(history) == 0 && e.baselineVersion != "" {
		if err := e.store.Record(ctx, Migration{Version: e.baselineVersion, Description: "<< Flyway Baseline >>", Script: "<< Flyway Baseline >>", Type: "BASELINE"}); err != nil {
			return nil, err
		}
		history, err = e.store.List(ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := e.validate(history, forwardOnly(ms)); err != nil {
		return nil, err
	}
	if e.callbacks.BeforeMigrateSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.BeforeMigrateSQL)); err != nil {
			return nil, err
		}
	}
	if e.callbacks.BeforeMigrate != nil {
		if err := e.callbacks.BeforeMigrate(ctx); err != nil {
			return nil, err
		}
	}

	applied := map[string]History{}
	baseline := ""
	maxApplied := ""
	for _, h := range history {
		if h.Type == "BASELINE" {
			baseline = h.Version
			continue
		}
		applied[h.Key()] = h
		if h.Version != "" && (maxApplied == "" || VersionCompare(h.Version, maxApplied) > 0) {
			maxApplied = h.Version
		}
	}

	var done []Migration
	for _, m := range forwardOnly(ms) {
		if e.cherryPick != "" && m.Version != e.cherryPick {
			continue
		}
		if h, ok := applied[m.Key()]; ok {
			if !m.Repeatable || (h.Checksum != nil && *h.Checksum == m.Checksum) {
				continue
			}
		}
		if baseline != "" && m.Version != "" && VersionCompare(m.Version, baseline) <= 0 {
			continue
		}
		if !e.outOfOrder && !m.Repeatable && maxApplied != "" && VersionCompare(m.Version, maxApplied) < 0 {
			return done, fmt.Errorf("%w: %s is lower than applied %s", ErrOutOfOrder, m.Version, maxApplied)
		}
		if e.callbacks.BeforeEachSQL != nil {
			if err := e.store.Exec(ctx, string(e.callbacks.BeforeEachSQL)); err != nil {
				return done, err
			}
		}
		if e.callbacks.BeforeEach != nil {
			if err := e.callbacks.BeforeEach(ctx, m); err != nil {
				return done, err
			}
		}
		if err := e.store.Apply(ctx, m); err != nil {
			return done, fmt.Errorf("%s %s: %w", m.Script, m.Version, err)
		}
		if e.callbacks.AfterEach != nil {
			if err := e.callbacks.AfterEach(ctx, m); err != nil {
				return done, err
			}
		}
		if e.callbacks.AfterEachSQL != nil {
			if err := e.store.Exec(ctx, string(e.callbacks.AfterEachSQL)); err != nil {
				return done, err
			}
		}
		done = append(done, m)
		if !m.Repeatable && (maxApplied == "" || VersionCompare(m.Version, maxApplied) > 0) {
			maxApplied = m.Version
		}
	}
	if e.callbacks.AfterMigrate != nil {
		if err := e.callbacks.AfterMigrate(ctx); err != nil {
			return done, err
		}
	}
	if e.callbacks.AfterMigrateSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.AfterMigrateSQL)); err != nil {
			return done, err
		}
	}
	return done, nil
}

// Baseline records a Flyway baseline row. It is allowed only when the history
// table is empty; migrations at or below version are then ignored by Up.
func (e *Engine) Baseline(ctx context.Context, version string) error {
	if err := validateVersion(version); err != nil {
		return err
	}
	if e.callbacks.BeforeBaselineSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.BeforeBaselineSQL)); err != nil {
			return err
		}
	}
	if e.callbacks.BeforeBaseline != nil {
		if err := e.callbacks.BeforeBaseline(ctx); err != nil {
			return err
		}
	}
	release, err := e.store.Lock(ctx, lockName)
	if err != nil {
		return err
	}
	defer release()
	if err := e.store.Ensure(ctx); err != nil {
		return err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return err
	}
	if len(history) != 0 {
		return ErrBaselineExists
	}
	if err := e.store.Record(ctx, Migration{Version: version, Description: "<< Flyway Baseline >>", Script: "<< Flyway Baseline >>", Type: "BASELINE"}); err != nil {
		return err
	}
	if e.callbacks.AfterBaseline != nil {
		return e.callbacks.AfterBaseline(ctx)
	}
	if e.callbacks.AfterBaselineSQL != nil {
		return e.store.Exec(ctx, string(e.callbacks.AfterBaselineSQL))
	}
	return nil
}

// Repair removes failed history rows and optionally realigns checksums to the
// provided source. A checksum mismatch is not silently accepted when no source
// is supplied.
func (e *Engine) Repair(ctx context.Context, ms ...Migration) error {
	if e.callbacks.BeforeRepairSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.BeforeRepairSQL)); err != nil {
			return err
		}
	}
	if e.callbacks.BeforeRepair != nil {
		if err := e.callbacks.BeforeRepair(ctx); err != nil {
			return err
		}
	}
	release, err := e.store.Lock(ctx, lockName)
	if err != nil {
		return err
	}
	defer release()
	if err := e.store.Ensure(ctx); err != nil {
		return err
	}
	if err := e.store.DeleteFailed(ctx); err != nil {
		return err
	}
	if len(ms) > 0 {
		history, err := e.store.List(ctx)
		if err != nil {
			return err
		}
		source := sourceByKey(ms)
		for _, h := range history {
			m, ok := source[h.Key()]
			if !ok || h.Checksum == nil || *h.Checksum == m.Checksum {
				continue
			}
			if err := e.store.UpdateChecksum(ctx, h.Script, m.Checksum); err != nil {
				return err
			}
		}
	}
	if e.callbacks.AfterRepair != nil {
		return e.callbacks.AfterRepair(ctx)
	}
	if e.callbacks.AfterRepairSQL != nil {
		return e.store.Exec(ctx, string(e.callbacks.AfterRepairSQL))
	}
	return nil
}

// Clean drops the objects owned by the store. It is disabled by default.
func (e *Engine) Clean(ctx context.Context) error {
	if e.cleanDisabled {
		return ErrCleanDisabled
	}
	if e.callbacks.BeforeCleanSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.BeforeCleanSQL)); err != nil {
			return err
		}
	}
	if e.callbacks.BeforeClean != nil {
		if err := e.callbacks.BeforeClean(ctx); err != nil {
			return err
		}
	}
	release, err := e.store.Lock(ctx, lockName)
	if err != nil {
		return err
	}
	defer release()
	if err := e.store.Clean(ctx); err != nil {
		return err
	}
	if e.callbacks.AfterClean != nil {
		return e.callbacks.AfterClean(ctx)
	}
	if e.callbacks.AfterCleanSQL != nil {
		return e.store.Exec(ctx, string(e.callbacks.AfterCleanSQL))
	}
	return nil
}

func (e *Engine) validate(history []History, ms []Migration) error {
	source := sourceByKey(ms)
	baseline := ""
	maxApplied := ""
	appliedVersions := map[string]bool{}
	appliedRepeatable := map[string]History{}
	for _, h := range history {
		if !h.Success {
			return fmt.Errorf("%w: failed migration %s (%s); run Repair after fixing it", ErrValidation, h.Version, h.Script)
		}
		if h.Type == "BASELINE" {
			baseline = h.Version
			continue
		}
		if h.Version == "" {
			if prev, ok := appliedRepeatable[h.Script]; !ok || h.InstalledRank > prev.InstalledRank {
				appliedRepeatable[h.Script] = h
			}
			continue
		}
		appliedVersions[h.Version] = true
		m, ok := source[h.Key()]
		if !ok {
			if e.ignoreMissing {
				continue
			}
			return fmt.Errorf("%w: applied migration %s (%s) is missing from the source", ErrValidation, h.Version, h.Script)
		}
		if h.Checksum != nil && m.Checksum != *h.Checksum {
			return fmt.Errorf("%w: checksum mismatch for %s: applied %d, source %d", ErrValidation, h.Version, *h.Checksum, m.Checksum)
		}
		if maxApplied == "" || VersionCompare(h.Version, maxApplied) > 0 {
			maxApplied = h.Version
		}
	}
	for script, h := range appliedRepeatable {
		if _, ok := source["R:"+script]; !ok {
			if e.ignoreMissing {
				continue
			}
			return fmt.Errorf("%w: applied repeatable migration %s is missing from the source", ErrValidation, script)
		}
		_ = h
	}
	if e.outOfOrder || maxApplied == "" {
		return nil
	}
	for _, m := range ms {
		if m.Repeatable {
			continue
		}
		if appliedVersions[m.Version] {
			continue
		}
		if baseline != "" && VersionCompare(m.Version, baseline) <= 0 {
			continue
		}
		if VersionCompare(m.Version, maxApplied) < 0 {
			return fmt.Errorf("%w: pending %s is lower than applied %s", ErrOutOfOrder, m.Version, maxApplied)
		}
	}
	return nil
}

// Undo reverses applied versioned migrations. With an empty target it undoes
// the latest applied version; with a target it undoes every applied version
// greater than target, newest first. The original history row is deleted, so a
// later Up can reapply the version.
func (e *Engine) Undo(ctx context.Context, ms []Migration, target string) ([]Migration, error) {
	undoSource := map[string]Migration{}
	for _, m := range ms {
		if m.Undo {
			undoSource[m.Version] = m
		}
	}
	release, err := e.store.Lock(ctx, lockName)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := e.store.Ensure(ctx); err != nil {
		return nil, err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return nil, err
	}
	var versions []string
	seen := map[string]bool{}
	for _, h := range history {
		if !h.Success || h.Type == "BASELINE" || h.Version == "" || seen[h.Version] {
			continue
		}
		if target != "" && VersionCompare(h.Version, target) <= 0 {
			continue
		}
		seen[h.Version] = true
		versions = append(versions, h.Version)
	}
	sort.Slice(versions, func(i, j int) bool { return VersionCompare(versions[i], versions[j]) > 0 })
	if target == "" && len(versions) > 1 {
		versions = versions[:1]
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("migrate: no applied version to undo")
	}
	if e.callbacks.BeforeUndoSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.BeforeUndoSQL)); err != nil {
			return nil, err
		}
	}
	if e.callbacks.BeforeUndo != nil {
		if err := e.callbacks.BeforeUndo(ctx); err != nil {
			return nil, err
		}
	}
	var done []Migration
	for _, version := range versions {
		m, ok := undoSource[version]
		if !ok {
			return done, fmt.Errorf("migrate: no U migration for %s", version)
		}
		if err := e.store.Undo(ctx, m); err != nil {
			return done, fmt.Errorf("%s: %w", m.Script, err)
		}
		done = append(done, m)
	}
	if e.callbacks.AfterUndo != nil {
		if err := e.callbacks.AfterUndo(ctx); err != nil {
			return done, err
		}
	}
	if e.callbacks.AfterUndoSQL != nil {
		if err := e.store.Exec(ctx, string(e.callbacks.AfterUndoSQL)); err != nil {
			return done, err
		}
	}
	return done, nil
}

func sourceByKey(ms []Migration) map[string]Migration {
	out := make(map[string]Migration, len(ms))
	for _, m := range forwardOnly(ms) {
		out[m.Key()] = m
	}
	return out
}

func forwardOnly(ms []Migration) []Migration {
	out := make([]Migration, 0, len(ms))
	for _, m := range ms {
		if !m.Undo {
			out = append(out, m)
		}
	}
	return out
}

// DryRun returns the migrations Up would apply, without executing or recording
// anything. Callbacks are not run; callbacks may have side effects.
func (e *Engine) DryRun(ctx context.Context, ms []Migration) ([]Migration, error) {
	if err := e.store.Ensure(ctx); err != nil {
		return nil, err
	}
	history, err := e.store.List(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.validate(history, forwardOnly(ms)); err != nil {
		return nil, err
	}
	applied := map[string]History{}
	baseline := ""
	maxApplied := ""
	for _, h := range history {
		if h.Type == "BASELINE" {
			baseline = h.Version
			continue
		}
		applied[h.Key()] = h
		if h.Version != "" && (maxApplied == "" || VersionCompare(h.Version, maxApplied) > 0) {
			maxApplied = h.Version
		}
	}
	var out []Migration
	for _, m := range forwardOnly(ms) {
		if e.cherryPick != "" && m.Version != e.cherryPick {
			continue
		}
		if h, ok := applied[m.Key()]; ok {
			if !m.Repeatable || (h.Checksum != nil && *h.Checksum == m.Checksum) {
				continue
			}
		}
		if baseline != "" && m.Version != "" && VersionCompare(m.Version, baseline) <= 0 {
			continue
		}
		if !e.outOfOrder && !m.Repeatable && maxApplied != "" && VersionCompare(m.Version, maxApplied) < 0 {
			return out, fmt.Errorf("%w: %s is lower than applied %s", ErrOutOfOrder, m.Version, maxApplied)
		}
		out = append(out, m)
		if !m.Repeatable && (maxApplied == "" || VersionCompare(m.Version, maxApplied) > 0) {
			maxApplied = m.Version
		}
	}
	return out, nil
}
