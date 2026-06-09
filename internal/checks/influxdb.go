package checks

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

	"sermo/internal/conn"
)

// influxCheck runs an InfluxQL query against an InfluxDB 1.x HTTP API and
// compares a scalar result against a value — the time-series counterpart of the
// sql/mongodb-query checks. It is condition-style: OK == true means the
// comparison holds. Connection variables (host/port/user/password/tls) mirror the
// influxdb connection check; the query runs against `database` over `GET /query`.
//
// By default the scalar is the last column of the first row of the first series
// (InfluxQL puts `time` first and the aggregate value last); set `column` to read
// a named column instead. Use a read-only user; the query is run as given.
type influxCheck struct {
	base
	cfg      conn.Config
	database string
	query    string
	column   string // optional named result column
	token    string // optional InfluxDB API token (Authorization: Token …)
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
	data := map[string]any{"database": c.database, "query": c.query, "op": c.op, "threshold": c.value, "result": result}
	if f, perr := strconv.ParseFloat(strings.TrimSpace(result), 64); perr == nil {
		data["value"] = f
	}
	res.Data = data
	return res
}

// queryScalar runs the InfluxQL query and returns the chosen scalar. The second
// return reports an empty/NULL result (no series, or a null cell).
func (c influxCheck) queryScalar(ctx context.Context, client *http.Client, base string) (string, bool, error) {
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
		idx = indexOf(cols, c.column)
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

// influxErrorBody extracts InfluxDB's JSON error message from a non-200 body,
// falling back to the trimmed raw body.
func influxErrorBody(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(body))
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// buildInfluxCheck builds an influxdb-query check, reusing the influxdb
// connection variables (host/port/user/password/tls) plus a database and an
// InfluxQL query.
func buildInfluxCheck(b base, entry map[string]any) (Check, string) {
	query := asString(entry["query"])
	if query == "" {
		return nil, "influxdb-query check requires a query"
	}
	database := asString(entry["database"])
	if database == "" {
		return nil, "influxdb-query check requires a database"
	}
	op := asString(entry["op"])
	if !validCompareOp(op) {
		return nil, "influxdb-query check op must be one of ==, !=, >, >=, <, <=, =~"
	}
	value := scalarString(entry["value"])
	if value == "" {
		return nil, "influxdb-query check requires a value"
	}
	return influxCheck{
		base:     b,
		cfg:      influxConnConfig(entry),
		database: database,
		query:    query,
		column:   asString(entry["column"]),
		token:    asString(entry["token"]),
		op:       op,
		value:    value,
	}, ""
}

// influxConnConfig builds a conn.Config for an influxdb-query check, defaulting
// the port to InfluxDB's standard port (via the conn registry).
func influxConnConfig(entry map[string]any) conn.Config {
	cfg := conn.Config{
		Host:     asString(entry["host"]),
		User:     asString(entry["user"]),
		Password: asString(entry["password"]),
		TLS:      tlsString(entry["tls"]),
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	cfg.Port = 8086
	if proto, ok := conn.Lookup("influxdb"); ok {
		cfg.Port = proto.DefaultPort()
	}
	if p, ok := intField(entry["port"]); ok {
		cfg.Port = p
	}
	return cfg
}
