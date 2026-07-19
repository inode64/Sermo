package checks

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/httpx"
)

const (
	influxAuthHeader      = httpx.HeaderAuthorization
	influxAuthTokenPrefix = "Token "
	influxFluxAccept      = "application/csv"
	influxFluxContentType = "application/vnd.flux"
	influxFluxQueryPath   = "/api/v2/query"
	influxQLQueryPath     = "/query"
)

// influxCheck runs an InfluxDB query and compares one scalar result with a
// threshold. It is condition-style: OK means the comparison holds. Use a
// read-only user or token.
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

	data := map[string]any{
		DataKeyLanguage: c.language,
		DataKeyQuery:    c.query,
	}
	if c.database != "" {
		data[DataKeyDatabase] = c.database
	}
	if c.org != "" {
		data[DataKeyOrg] = c.org
	}
	return finishScalarCompare(c.base, "influxdb", result, c.op, c.value, start, data)
}

// queryScalar runs the query and returns the chosen scalar. The second return
// reports an empty result (no data, or a null/empty cell).
func (c influxCheck) queryScalar(ctx context.Context, client *http.Client, base string) (string, bool, error) {
	if c.language == InfluxLanguageFlux {
		return c.fluxScalar(ctx, client, base)
	}
	return c.influxqlScalar(ctx, client, base)
}

// influxqlScalar runs the InfluxQL query over the 1.x GET /query API.
func (c influxCheck) influxqlScalar(ctx context.Context, client *http.Client, base string) (string, bool, error) {
	q := url.Values{"db": {c.database}, "q": {c.query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+influxQLQueryPath+"?"+q.Encode(), http.NoBody)
	if err != nil {
		return "", false, err
	}
	switch {
	case c.token != "":
		req.Header.Set(influxAuthHeader, influxAuthTokenPrefix+c.token)
	case c.cfg.User != "":
		req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
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
	u := base + influxFluxQueryPath + "?org=" + url.QueryEscape(c.org)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(c.query))
	if err != nil {
		return "", false, err
	}
	req.Header.Set(influxAuthHeader, influxAuthTokenPrefix+c.token)
	req.Header.Set(httpHeaderContentType, influxFluxContentType)
	req.Header.Set(httpHeaderAccept, influxFluxAccept)

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("HTTP status %d: %s", resp.StatusCode, influxErrorBody(body))
	}

	// Annotated CSV: '#'-prefixed annotation lines, then a header row and data
	// rows. Read the first header + first data row of the first result block.
	cr := csv.NewReader(bytes.NewReader(body))
	cr.Comment = '#'
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return "", true, nil // no result
	}
	if err != nil {
		return "", false, fmt.Errorf("invalid CSV response: %w", err)
	}
	row, err := cr.Read()
	if errors.Is(err, io.EOF) {
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
	query := cfgval.AsString(entry[CheckKeyQuery])
	if query == "" {
		return nil, "influxdb-query check requires a query"
	}
	op, value, msg := assertOpValue(entry, CheckTypeInfluxDBQuery)
	if msg != "" {
		return nil, msg
	}
	language := cfgval.AsString(entry[CheckKeyLanguage])
	if language == "" {
		language = InfluxLanguageInfluxQL
	}

	c := influxCheck{
		base:     b,
		cfg:      influxConnConfig(entry),
		language: language,
		database: cfgval.AsString(entry[CheckKeyDatabase]),
		org:      cfgval.AsString(entry[CheckKeyOrg]),
		query:    query,
		column:   cfgval.AsString(entry[CheckKeyColumn]),
		token:    cfgval.AsString(entry[CheckKeyToken]),
		op:       op,
		value:    value,
	}
	switch language {
	case InfluxLanguageInfluxQL:
		if c.database == "" {
			return nil, "influxdb-query (influxql) check requires a database"
		}
	case InfluxLanguageFlux:
		if c.org == "" {
			return nil, "influxdb-query (flux) check requires an org"
		}
		if c.token == "" {
			return nil, "influxdb-query (flux) check requires a token"
		}
	default:
		return nil, "influxdb-query check language must be " + InfluxLanguageSummary
	}
	return c, ""
}

// influxConnConfig builds a conn.Config for an influxdb-query check, defaulting
// the port to InfluxDB's standard port (via the conn registry).
func influxConnConfig(entry map[string]any) conn.Config {
	cfg := baseConnectionConfig(entry)
	cfg.Port = connectionPort(entry, conn.DefaultPort(conn.ProtocolNameInfluxDB))
	return cfg
}
