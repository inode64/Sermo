package checks

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// influxCheck runs a query against an InfluxDB HTTP API and compares a scalar
// result against a value — the time-series counterpart of the sql/mongodb-query
// checks. It is condition-style: OK == true means the comparison holds.
// Connection variables (host/port/user/password/tls) mirror the influxdb
// connection check. Two query languages are supported:
//   - influxql (default): InfluxDB 1.x `GET /query` against `database`; the JSON
//     result's scalar is the last column of the first row (or the named `column`).
//   - flux: InfluxDB 2.x `POST /api/v2/query` against `org` with a `token`; the
//     annotated-CSV result's scalar is the `_value` column of the first data row
//     (or the named `column`).
//
// Use a read-only user/token; the query is run as given.
type influxCheck struct {
	base
	cfg      conn.Config
	language string // influxql | flux
	database string // influxql: the database
	org      string // flux: the organization
	query    string
	column   string // optional named result column
	token    string // API token (required for flux; optional v1.8+ for influxql)
	op       string
	value    string
}

func (c influxCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	client, base := conn.InfluxClient(c.cfg)
	result, isNull, err := c.queryScalar(ctx, client, base)
	if err != nil {
		return c.result(false, "influxdb: "+err.Error(), start)
	}
	if isNull {
		return c.result(false, "influxdb: query returned no value", start)
	}

	ok, err := compareValue(result, c.op, c.value)
	if err != nil {
		return c.result(false, "influxdb: "+err.Error(), start)
	}
	res := c.result(ok, fmt.Sprintf("influxdb: %q %s %q = %t", result, c.op, c.value, ok), start)
	data := map[string]any{"language": c.language, "query": c.query, "op": c.op, "threshold": c.value, "result": result}
	if c.database != "" {
		data["database"] = c.database
	}
	if c.org != "" {
		data["org"] = c.org
	}
	if f, perr := strconv.ParseFloat(strings.TrimSpace(result), 64); perr == nil {
		data["value"] = f
	}
	res.Data = data
	return res
}

// queryScalar runs the query and returns the chosen scalar. The second return
// reports an empty result (no data, or a null/empty cell).
func (c influxCheck) queryScalar(ctx context.Context, client *http.Client, base string) (string, bool, error) {
	if c.language == "flux" {
		return c.fluxScalar(ctx, client, base)
	}
	return c.influxqlScalar(ctx, client, base)
}

// influxqlScalar runs the InfluxQL query over the 1.x GET /query API.
func (c influxCheck) influxqlScalar(ctx context.Context, client *http.Client, base string) (string, bool, error) {
	q := url.Values{"db": {c.database}, "q": {c.query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/query?"+q.Encode(), nil)
	if err != nil {
		return "", false, err
	}
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Token "+c.token)
	case c.cfg.User != "":
		req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, influxErrorBody(body))
	}

	var out struct {
		Error   string `json:"error"` // top-level (auth/parse) error
		Results []struct {
			Error  string `json:"error"` // per-statement error
			Series []struct {
				Columns []string `json:"columns"`
				Values  [][]any  `json:"values"`
			} `json:"series"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("invalid JSON response: %w", err)
	}
	if out.Error != "" {
		return "", false, fmt.Errorf("query error: %s", out.Error)
	}
	if len(out.Results) == 0 {
		return "", true, nil
	}
	r := out.Results[0]
	if r.Error != "" {
		return "", false, fmt.Errorf("query error: %s", r.Error)
	}
	if len(r.Series) == 0 || len(r.Series[0].Values) == 0 {
		return "", true, nil // matched nothing
	}
	cols, row := r.Series[0].Columns, r.Series[0].Values[0]

	idx := len(row) - 1 // default: the last column (the value; `time` is first)
	if c.column != "" {
		idx = slices.Index(cols, c.column)
		if idx < 0 || idx >= len(row) {
			return "", false, fmt.Errorf("column %q not found in result", c.column)
		}
	}
	if idx < 0 {
		return "", true, nil
	}
	if row[idx] == nil {
		return "", true, nil
	}
	return jsonValueString(row[idx]), false, nil
}

// fluxScalar runs the Flux query over the 2.x POST /api/v2/query API and reads a
// scalar from the annotated-CSV response (the `_value` column by default).
func (c influxCheck) fluxScalar(ctx context.Context, client *http.Client, base string) (string, bool, error) {
	u := base + "/api/v2/query?org=" + url.QueryEscape(c.org)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(c.query))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Content-Type", "application/vnd.flux")
	req.Header.Set("Accept", "application/csv")

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, influxErrorBody(body))
	}

	// Annotated CSV: '#'-prefixed annotation lines, then a header row and data
	// rows. Read the first header + first data row of the first result block.
	cr := csv.NewReader(bytes.NewReader(body))
	cr.Comment = '#'
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err == io.EOF {
		return "", true, nil // no result
	}
	if err != nil {
		return "", false, fmt.Errorf("invalid CSV response: %w", err)
	}
	row, err := cr.Read()
	if err == io.EOF {
		return "", true, nil // header but no data
	}
	if err != nil {
		return "", false, fmt.Errorf("invalid CSV response: %w", err)
	}

	col := c.column
	if col == "" {
		col = "_value" // Flux's value column
	}
	idx := slices.Index(header, col)
	if idx < 0 || idx >= len(row) {
		return "", false, fmt.Errorf("column %q not found in result", col)
	}
	if row[idx] == "" {
		return "", true, nil
	}
	return row[idx], false, nil
}

// influxErrorBody extracts InfluxDB's JSON error message from a non-200 body
// (1.x uses `error`, 2.x uses `message`), falling back to the trimmed raw body.
func influxErrorBody(body []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Error != "" {
			return e.Error
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return strings.TrimSpace(string(body))
}

// buildInfluxCheck builds an influxdb-query check, reusing the influxdb
// connection variables (host/port/user/password/tls). `language` selects InfluxQL
// (1.x, needs a `database`) or Flux (2.x, needs an `org` and `token`).
func buildInfluxCheck(b base, entry map[string]any) (Check, string) {
	query := cfgval.AsString(entry["query"])
	if query == "" {
		return nil, "influxdb-query check requires a query"
	}
	op := cfgval.AsString(entry["op"])
	if !validCompareOp(op) {
		return nil, "influxdb-query check op must be one of ==, !=, >, >=, <, <=, =~"
	}
	value := cfgval.String(entry["value"])
	if value == "" {
		return nil, "influxdb-query check requires a value"
	}
	language := cfgval.AsString(entry["language"])
	if language == "" {
		language = "influxql"
	}

	c := influxCheck{
		base:     b,
		cfg:      influxConnConfig(entry),
		language: language,
		database: cfgval.AsString(entry["database"]),
		org:      cfgval.AsString(entry["org"]),
		query:    query,
		column:   cfgval.AsString(entry["column"]),
		token:    cfgval.AsString(entry["token"]),
		op:       op,
		value:    value,
	}
	switch language {
	case "influxql":
		if c.database == "" {
			return nil, "influxdb-query (influxql) check requires a database"
		}
	case "flux":
		if c.org == "" {
			return nil, "influxdb-query (flux) check requires an org"
		}
		if c.token == "" {
			return nil, "influxdb-query (flux) check requires a token"
		}
	default:
		return nil, "influxdb-query check language must be influxql or flux"
	}
	return c, ""
}

// influxConnConfig builds a conn.Config for an influxdb-query check, defaulting
// the port to InfluxDB's standard port (via the conn registry).
func influxConnConfig(entry map[string]any) conn.Config {
	cfg := conn.Config{
		Host:     cfgval.AsString(entry["host"]),
		User:     cfgval.AsString(entry["user"]),
		Password: cfgval.AsString(entry["password"]),
		TLS:      tlsString(entry["tls"]),
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	cfg.Port = 8086
	if proto, ok := conn.Lookup("influxdb"); ok {
		cfg.Port = proto.DefaultPort()
	}
	if p, ok := cfgval.Int(entry["port"]); ok {
		cfg.Port = p
	}
	return cfg
}
