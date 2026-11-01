package gmigrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

var callbackFiles = map[string]string{
	"beforeMigrate.sql": "BeforeMigrateSQL", "afterMigrate.sql": "AfterMigrateSQL",
	"beforeEachMigrate.sql": "BeforeEachSQL", "afterEachMigrate.sql": "AfterEachSQL",
	"beforeValidate.sql": "BeforeValidateSQL", "afterValidate.sql": "AfterValidateSQL",
	"beforeBaseline.sql": "BeforeBaselineSQL", "afterBaseline.sql": "AfterBaselineSQL",
	"beforeRepair.sql": "BeforeRepairSQL", "afterRepair.sql": "AfterRepairSQL",
	"beforeClean.sql": "BeforeCleanSQL", "afterClean.sql": "AfterCleanSQL",
}

// LoadCallbacksDir loads Flyway-style callback SQL files from a directory.
func LoadCallbacksDir(dir string, opts ...SourceOption) (Callbacks, error) {
	return LoadCallbacks(os.DirFS(dir), opts...)
}

// LoadCallbacks loads before/after callback files from the root of fsys.
// Unknown callback files are ignored; V/R migration files are never treated as
// callbacks.
func LoadCallbacks(fsys fs.FS, opts ...SourceOption) (Callbacks, error) {
	options := sourceOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	callbacks := Callbacks{}
	for name, field := range callbackFiles {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Callbacks{}, fmt.Errorf("callback %s: %w", name, err)
		}
		executable, err := replacePlaceholders(string(data), options.placeholders, options.placeholdersSet)
		if err != nil {
			return Callbacks{}, fmt.Errorf("callback %s: %w", name, err)
		}
		setCallbackSQL(&callbacks, field, []byte(executable))
	}
	return callbacks, nil
}

func setCallbackSQL(c *Callbacks, field string, data []byte) {
	switch field {
	case "BeforeMigrateSQL":
		c.BeforeMigrateSQL = data
	case "AfterMigrateSQL":
		c.AfterMigrateSQL = data
	case "BeforeEachSQL":
		c.BeforeEachSQL = data
	case "AfterEachSQL":
		c.AfterEachSQL = data
	case "BeforeValidateSQL":
		c.BeforeValidateSQL = data
	case "AfterValidateSQL":
		c.AfterValidateSQL = data
	case "BeforeBaselineSQL":
		c.BeforeBaselineSQL = data
	case "AfterBaselineSQL":
		c.AfterBaselineSQL = data
	case "BeforeRepairSQL":
		c.BeforeRepairSQL = data
	case "AfterRepairSQL":
		c.AfterRepairSQL = data
	case "BeforeCleanSQL":
		c.BeforeCleanSQL = data
	case "AfterCleanSQL":
		c.AfterCleanSQL = data
	}
}

func isCallbackFile(name string) bool {
	_, ok := callbackFiles[name]
	return ok
}

// mergeCallbacks fills only fields that are still empty, so explicit Go
// callbacks configured by the caller win over files.
func mergeCallbacks(dst *Callbacks, src Callbacks) {
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
