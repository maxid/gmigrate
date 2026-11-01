// Command gmigrate is the standalone migration CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/maxid/gmigrate"
)

type options struct {
	url               string
	locations         string
	table             string
	schema            string
	installedBy       string
	baselineVersion   string
	outOfOrder        bool
	ignoreMissing     bool
	baselineOnMigrate bool
	cleanDisabled     bool
	placeholders      placeholderFlags
}

type placeholderFlags map[string]string

func (p placeholderFlags) String() string { return "" }
func (p placeholderFlags) Set(value string) error {
	key, val, ok := strings.Cut(value, "=")
	if !ok || key == "" {
		return fmt.Errorf("want key=value, got %q", value)
	}
	p[key] = val
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gmigrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	opts := options{cleanDisabled: true, placeholders: placeholderFlags{}}
	fs := flag.NewFlagSet("gmigrate", flag.ContinueOnError)
	fs.StringVar(&opts.url, "url", "", "database URL (mysql://, postgres://, clickhouse://, tdengine://)")
	fs.StringVar(&opts.locations, "locations", "db/migrations", "comma-separated migration directories")
	fs.StringVar(&opts.table, "table", gmigrate.DefaultHistoryTable, "history table")
	fs.StringVar(&opts.schema, "schema", "", "default schema/database")
	fs.StringVar(&opts.installedBy, "installed-by", "gmigrate", "installed_by value")
	fs.StringVar(&opts.baselineVersion, "baseline-version", "", "baseline version")
	fs.BoolVar(&opts.outOfOrder, "out-of-order", false, "allow out-of-order migrations")
	fs.BoolVar(&opts.ignoreMissing, "ignore-missing", false, "ignore missing applied migrations")
	fs.BoolVar(&opts.baselineOnMigrate, "baseline-on-migrate", false, "record baseline-version on an empty history")
	fs.BoolVar(&opts.cleanDisabled, "clean-disabled", true, "disable the destructive clean command")
	fs.Var(opts.placeholders, "placeholder", "placeholder key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("command required: migrate|info|validate|baseline|repair|clean")
	}
	command := fs.Arg(0)
	if opts.url == "" {
		return fmt.Errorf("-url is required")
	}
	store, err := openStore(opts.url, opts.table, opts.installedBy, opts.schema)
	if err != nil {
		return err
	}
	defer store.Close()

	ms, err := loadMigrations(opts)
	if err != nil {
		return err
	}
	callbacks, err := loadCallbacks(opts)
	if err != nil {
		return err
	}
	engine, err := gmigrate.New(store, gmigrate.Config{
		Callbacks:         callbacks,
		OutOfOrder:        opts.outOfOrder,
		IgnoreMissing:     opts.ignoreMissing,
		BaselineOnMigrate: opts.baselineOnMigrate,
		BaselineVersion:   opts.baselineVersion,
		CleanDisabled:     opts.cleanDisabled,
	})
	if err != nil {
		return err
	}

	switch command {
	case "migrate", "up":
		done, err := engine.Migrate(ctx, ms)
		if err != nil {
			return err
		}
		fmt.Printf("applied %d migration(s)\n", len(done))
		for _, m := range done {
			fmt.Printf("  %s %s\n", m.Version, m.Description)
		}
	case "info":
		rows, err := engine.Info(ctx, ms)
		if err != nil {
			return err
		}
		fmt.Printf("%-10s %-12s %-8s %s\n", "VERSION", "STATE", "TYPE", "DESCRIPTION")
		for _, row := range rows {
			version := row.Version
			if version == "" {
				version = "(repeatable)"
			}
			fmt.Printf("%-10s %-12s %-8s %s\n", version, row.State, row.Type, row.Description)
		}
	case "validate":
		if err := engine.Validate(ctx, ms); err != nil {
			return err
		}
		fmt.Println("validation OK")
	case "baseline":
		if opts.baselineVersion == "" {
			return fmt.Errorf("-baseline-version is required for baseline")
		}
		if err := engine.Baseline(ctx, opts.baselineVersion); err != nil {
			return err
		}
		fmt.Println("baseline recorded")
	case "repair":
		if err := engine.Repair(ctx, ms...); err != nil {
			return err
		}
		fmt.Println("repair complete")
	case "clean":
		if err := engine.Clean(ctx); err != nil {
			return err
		}
		fmt.Println("clean complete")
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	return nil
}

func loadMigrations(opts options) ([]gmigrate.Migration, error) {
	var ms []gmigrate.Migration
	for _, dir := range strings.Split(opts.locations, ",") {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		found, err := gmigrate.LoadDir(dir, gmigrate.WithPlaceholders(opts.placeholders))
		if err != nil {
			return nil, err
		}
		ms = append(ms, found...)
	}
	ms = append(ms, gmigrate.Registered()...)
	return gmigrate.SortMigrations(ms), nil
}

func loadCallbacks(opts options) (gmigrate.Callbacks, error) {
	var out gmigrate.Callbacks
	for _, dir := range strings.Split(opts.locations, ",") {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		callbacks, err := gmigrate.LoadCallbacksDir(dir, gmigrate.WithPlaceholders(opts.placeholders))
		if err != nil {
			return gmigrate.Callbacks{}, err
		}
		mergeCallbacks(&out, callbacks)
	}
	return out, nil
}

func mergeCallbacks(dst *gmigrate.Callbacks, src gmigrate.Callbacks) {
	if dst.BeforeMigrateSQL == nil {
		dst.BeforeMigrateSQL = src.BeforeMigrateSQL
	}
	if dst.AfterMigrateSQL == nil {
		dst.AfterMigrateSQL = src.AfterMigrateSQL
	}
	if dst.BeforeEachSQL == nil {
		dst.BeforeEachSQL = src.BeforeEachSQL
	}
	if dst.AfterEachSQL == nil {
		dst.AfterEachSQL = src.AfterEachSQL
	}
	if dst.BeforeValidateSQL == nil {
		dst.BeforeValidateSQL = src.BeforeValidateSQL
	}
	if dst.AfterValidateSQL == nil {
		dst.AfterValidateSQL = src.AfterValidateSQL
	}
	if dst.BeforeBaselineSQL == nil {
		dst.BeforeBaselineSQL = src.BeforeBaselineSQL
	}
	if dst.AfterBaselineSQL == nil {
		dst.AfterBaselineSQL = src.AfterBaselineSQL
	}
	if dst.BeforeRepairSQL == nil {
		dst.BeforeRepairSQL = src.BeforeRepairSQL
	}
	if dst.AfterRepairSQL == nil {
		dst.AfterRepairSQL = src.AfterRepairSQL
	}
	if dst.BeforeCleanSQL == nil {
		dst.BeforeCleanSQL = src.BeforeCleanSQL
	}
	if dst.AfterCleanSQL == nil {
		dst.AfterCleanSQL = src.AfterCleanSQL
	}
}

func openStore(raw, table, installedBy, schema string) (gmigrate.Store, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("invalid database URL")
	}
	opts := []gmigrate.Option{gmigrate.WithTable(table), gmigrate.WithInstalledBy(installedBy)}
	if schema != "" {
		opts = append(opts, gmigrate.WithSchema(schema))
	}
	switch u.Scheme {
	case "mysql":
		dsn := u.User.String() + "@tcp(" + u.Host + ")/" + strings.TrimPrefix(u.Path, "/")
		if u.RawQuery != "" {
			dsn += "?" + u.RawQuery
		}
		return gmigrate.OpenMySQL(dsn, opts...)
	case "postgres", "postgresql":
		return gmigrate.OpenPostgres(raw, opts...)
	case "clickhouse":
		return gmigrate.OpenClickHouse(raw, opts...)
	case "tdengine":
		return gmigrate.OpenTDengine(raw, opts...)
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
}
