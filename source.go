package gmigrate

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// SourceOption configures SQL loading.
type SourceOption func(*sourceOptions)

type sourceOptions struct {
	placeholders    map[string]string
	placeholdersSet bool
}

// WithPlaceholders applies ${key} substitutions after checksum calculation and
// before execution, matching Flyway's placeholder behavior.
func WithPlaceholders(values map[string]string) SourceOption {
	return func(o *sourceOptions) {
		o.placeholdersSet = true
		o.placeholders = make(map[string]string, len(values))
		for k, v := range values {
			o.placeholders[k] = v
		}
	}
}

// LoadDir loads migrations from a directory on disk.
func LoadDir(dir string, opts ...SourceOption) ([]Migration, error) {
	return LoadFS(os.DirFS(dir), opts...)
}

var (
	versionedRE   = regexp.MustCompile(`^V([0-9][0-9.]*)__(.+)\.sql$`)
	repeatableRE  = regexp.MustCompile(`^R__(.+)\.sql$`)
	placeholderRE = regexp.MustCompile(`\$\{([A-Za-z0-9_.-]+)\}`)
)

// LoadFS walks fsys, parses V<version>__<description>.sql and
// R__<description>.sql files, and returns versioned migrations first (by
// version) followed by repeatable migrations (by script). Versions are unique
// across the tree, matching Flyway's one-history-table-per-schema model.
func LoadFS(fsys fs.FS, opts ...SourceOption) ([]Migration, error) {
	options := sourceOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	var out []Migration
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return nil
		}
		script := path.Clean(p)
		name := path.Base(script)
		version, description := "", ""
		repeatable := false
		if m := versionedRE.FindStringSubmatch(name); m != nil {
			version, description = m[1], m[2]
			if err := validateVersion(version); err != nil {
				return fmt.Errorf("migration %s: %w", script, err)
			}
		} else if m := repeatableRE.FindStringSubmatch(name); m != nil {
			description = m[1]
			repeatable = true
		} else {
			return fmt.Errorf("migration %s: invalid name (want V<version>__<description>.sql or R__<description>.sql)", script)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		executable, err := replacePlaceholders(string(data), options.placeholders, options.placeholdersSet)
		if err != nil {
			return fmt.Errorf("migration %s: %w", script, err)
		}
		out = append(out, Migration{
			Version:     version,
			Description: description,
			Script:      script,
			Type:        "SQL",
			Checksum:    Checksum(data),
			SQL:         []byte(executable),
			Repeatable:  repeatable,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
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
	seen := map[string]string{}
	for _, m := range out {
		key := m.Key()
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate migration %s: %s and %s", key, prev, m.Script)
		}
		seen[key] = m.Script
	}
	return out, nil
}

// SortMigrations orders versioned migrations by version and repeatable ones
// after them by script. It does not mutate the input.
func SortMigrations(ms []Migration) []Migration {
	out := append([]Migration(nil), ms...)
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

func replacePlaceholders(sql string, values map[string]string, enabled bool) (string, error) {
	if !enabled {
		return sql, nil
	}
	var missing []string
	out := placeholderRE.ReplaceAllStringFunc(sql, func(token string) string {
		key := token[2 : len(token)-1]
		if value, ok := values[key]; ok {
			return value
		}
		missing = append(missing, key)
		return token
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("%w: unresolved placeholders %v", ErrInvalidConfig, missing)
	}
	return out, nil
}

// Checksum computes Flyway's checksum: CRC32 over each UTF-8 line without its
// line ending. The result is cast to int32, exactly as Flyway stores it, so an
// existing Flyway history row keeps the same checksum.
func Checksum(data []byte) int32 {
	h := crc32.NewIEEE()
	reader := bufio.NewReader(bytes.NewReader(data))
	first := true
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if first {
				line = strings.TrimPrefix(line, "\uFEFF")
				first = false
			}
			_, _ = h.Write([]byte(line))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return int32(h.Sum32())
		}
	}
	return int32(h.Sum32())
}
