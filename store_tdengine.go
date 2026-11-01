package gmigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TDengineStore applies migrations through the TDengine REST SQL endpoint
// (default port 6041). TDengine has no transactions or advisory locks, so
// migrations must be idempotent and only one migrator may run.
type TDengineStore struct {
	baseURL string
	user    string
	pass    string
	db      string
	client  *http.Client
	table   string
	by      string
	schema  string
}

func (s *TDengineStore) tableName() string { return s.table }

// OpenTDengine parses a DSN of the form:
//
//	http://user:pass@host:6041/database
func OpenTDengine(dsn string, opts ...Option) (Store, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid TDengine DSN (want http://user:pass@host:6041/db)", ErrInvalidConfig)
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
	s := &TDengineStore{
		baseURL: u.Scheme + "://" + u.Host,
		db:      strings.Trim(u.Path, "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
		table:   cfg.table,
		by:      cfg.installedBy,
		schema:  schema,
	}
	if s.schema != "" {
		s.db = s.schema
	}
	if u.User != nil {
		s.user = u.User.Username()
		s.pass, _ = u.User.Password()
	}
	return s, nil
}

func (s *TDengineStore) Close() error { return nil }

func (s *TDengineStore) Ensure(ctx context.Context) error {
	_, err := s.exec(ctx, `CREATE TABLE IF NOT EXISTS `+s.tableName()+` (
  ts          TIMESTAMP,
  installed_rank INT,
  version     NCHAR(64),
  description NCHAR(255),
  type        NCHAR(20),
  script      NCHAR(1000),
  checksum    NCHAR(32),
  installed_by NCHAR(100),
  installed_on NCHAR(32),
  execution_time INT,
  success     NCHAR(8)
)`)
	return err
}

func (s *TDengineStore) List(ctx context.Context) ([]History, error) {
	resp, err := s.exec(ctx, `SELECT installed_rank, version, description, type, script, checksum, installed_by, installed_on, execution_time, success FROM `+s.tableName()+` ORDER BY installed_rank`)
	if err != nil {
		return nil, err
	}
	var out []History
	for _, row := range resp.Data {
		if len(row) < 10 {
			continue
		}
		rank, _ := strconv.Atoi(asString(row[0]))
		h := History{
			InstalledRank: rank,
			Version:       asString(row[1]),
			Description:   asString(row[2]),
			Type:          asString(row[3]),
			Script:        asString(row[4]),
			InstalledBy:   asString(row[6]),
			InstalledOn:   asString(row[7]),
			Success:       asString(row[9]) == "true" || asString(row[9]) == "TRUE" || asString(row[9]) == "1",
		}
		h.ExecutionTime, _ = strconv.Atoi(asString(row[8]))
		if checksum := asString(row[5]); checksum != "" {
			n, err := strconv.ParseInt(checksum, 10, 32)
			if err == nil {
				v := int32(n)
				h.Checksum = &v
			}
		}
		out = append(out, h)
	}
	return out, nil
}

func (s *TDengineStore) nextRank(ctx context.Context) (int, error) {
	resp, err := s.exec(ctx, `SELECT MAX(installed_rank) FROM `+s.tableName())
	if err != nil {
		return 0, err
	}
	if len(resp.Data) == 0 || len(resp.Data[0]) == 0 {
		return 1, nil
	}
	n, _ := strconv.Atoi(asString(resp.Data[0][0]))
	return n + 1, nil
}

func (s *TDengineStore) Apply(ctx context.Context, m Migration) error {
	if m.Go != nil {
		return fmt.Errorf("%w: Go migrations cannot run on TDengine", ErrUnsupported)
	}
	started := time.Now()
	for _, stmt := range splitStatements(string(m.SQL)) {
		if _, err := s.exec(ctx, stmt); err != nil {
			return fmt.Errorf("%s %s: %w", m.Script, m.Version, err)
		}
	}
	rank, err := s.nextRank(ctx)
	if err != nil {
		return err
	}
	checksum := ""
	if m.Checksum != 0 {
		checksum = strconv.FormatInt(int64(m.Checksum), 10)
	}
	stmt := fmt.Sprintf(`INSERT INTO %s (ts, installed_rank, version, description, type, script, checksum, installed_by, installed_on, execution_time, success) VALUES (NOW, %d, %s, %s, %s, %s, %s, %s, %s, %d, 'true')`,
		s.tableName(), rank, sqlString(m.Version), sqlString(m.Description), sqlString(m.migrationType()), sqlString(m.Script), sqlString(checksum), sqlString(s.by), sqlString(time.Now().UTC().Format(time.RFC3339)), int(time.Since(started).Milliseconds()))
	_, err = s.exec(ctx, stmt)
	return err
}

func (s *TDengineStore) Record(ctx context.Context, m Migration) error {
	rank, err := s.nextRank(ctx)
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf(`INSERT INTO %s (ts, installed_rank, version, description, type, script, checksum, installed_by, installed_on, execution_time, success) VALUES (NOW, %d, %s, %s, %s, %s, NULL, %s, %s, 0, 'true')`,
		s.tableName(), rank, sqlString(m.Version), sqlString(m.Description), sqlString(m.migrationType()), sqlString(m.Script), sqlString(s.by), sqlString(time.Now().UTC().Format(time.RFC3339)))
	_, err = s.exec(ctx, stmt)
	return err
}

func (s *TDengineStore) Exec(ctx context.Context, sql string) error {
	for _, stmt := range splitStatements(sql) {
		if _, err := s.exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *TDengineStore) DeleteFailed(context.Context) error {
	return fmt.Errorf("%w: repair is not supported on TDengine", ErrUnsupported)
}

func (s *TDengineStore) UpdateChecksum(context.Context, string, int32) error {
	return fmt.Errorf("%w: checksum repair is not supported on TDengine", ErrUnsupported)
}

func (s *TDengineStore) Clean(ctx context.Context) error {
	_, err := s.exec(ctx, `DROP TABLE IF EXISTS `+s.tableName())
	return err
}

func (s *TDengineStore) Lock(context.Context, string) (func(), error) {
	return func() {}, nil
}

type tdResponse struct {
	Status string          `json:"status"`
	Code   int             `json:"code"`
	Desc   string          `json:"desc"`
	Head   []string        `json:"head"`
	Data   [][]any         `json:"data"`
	Rows   int             `json:"rows"`
	Raw    json.RawMessage `json:"-"`
}

func (s *TDengineStore) exec(ctx context.Context, statement string) (tdResponse, error) {
	endpoint := s.baseURL + "/rest/sql"
	if s.db != "" {
		endpoint += "?db=" + url.QueryEscape(s.db)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(statement))
	if err != nil {
		return tdResponse{}, err
	}
	if s.user != "" {
		req.SetBasicAuth(s.user, s.pass)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return tdResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return tdResponse{}, err
	}
	var out tdResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return tdResponse{}, fmt.Errorf("tdengine: decode response: %w", err)
	}
	out.Raw = body
	if resp.StatusCode/100 != 2 || out.Status == "error" || out.Code != 0 {
		return out, fmt.Errorf("tdengine: %s (status %d, code %d)", out.Desc, resp.StatusCode, out.Code)
	}
	return out, nil
}

func asString(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func sqlString(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func splitStatements(sql string) []string {
	var out []string
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			if inQuote && i+1 < len(sql) && sql[i+1] == '\'' {
				current.WriteByte(c)
				current.WriteByte(sql[i+1])
				i++
				continue
			}
			inQuote = !inQuote
		}
		if c == ';' && !inQuote {
			if stmt := strings.TrimSpace(current.String()); stmt != "" {
				out = append(out, stmt)
			}
			current.Reset()
			continue
		}
		current.WriteByte(c)
	}
	if stmt := strings.TrimSpace(current.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

var _ Store = (*TDengineStore)(nil)
