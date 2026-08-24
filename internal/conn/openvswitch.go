package conn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// openvswitchProtocol probes Open vSwitch's configuration database server
// (ovsdb-server) over the OVSDB management protocol (RFC 7047), a JSON-RPC
// dialogue. It issues a `list_dbs` request and requires the `Open_vSwitch`
// database, then follows up with a `transact` select that must read its root row
// from the Open_vSwitch table. This proves the intended database is served and
// readable, rather than merely proving that an OVSDB endpoint is listening. The
// optional `ovs_version` is reported when populated. ovsdb-server listens on a
// Unix socket (set `socket`, commonly
// /run/openvswitch/db.sock) or TCP (default port 6640); `tls` enables SSL.
// No auth.
type openvswitchProtocol struct{}

func (openvswitchProtocol) Name() string       { return ProtocolNameOpenVSwitch }
func (openvswitchProtocol) DefaultPort() int   { return defaultPortOpenVSwitch }
func (openvswitchProtocol) RequiresUser() bool { return false }

const (
	ovsdbColumnVersion       = "ovs_version"
	ovsdbDatabaseOpenVSwitch = "Open_vSwitch"
	ovsdbFieldColumns        = "columns"
	ovsdbFieldID             = "id"
	ovsdbFieldMethod         = "method"
	ovsdbFieldOp             = "op"
	ovsdbFieldParams         = "params"
	ovsdbFieldTable          = "table"
	ovsdbFieldWhere          = "where"
	ovsdbIDListDatabases     = "0"
	ovsdbIDTransact          = "1"
	ovsdbJSONNull            = "null"
	ovsdbMethodListDatabases = "list_dbs"
	ovsdbMethodTransact      = "transact"
	ovsdbOpSelect            = "select"
	ovsdbProbeMaxResponses   = 8
	ovsdbTableOpenVSwitch    = ovsdbDatabaseOpenVSwitch

	ovsdbFirstResultIndex = 0
	ovsdbFirstRowIndex    = 0
)

func (openvswitchProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := probeTargetFor(ctx, cfg, defaultPortOpenVSwitch).openStream(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)

	// list_dbs proves the server is up and reports the databases it serves.
	var dbs []string
	if err := ovsdbCall(enc, dec, ovsdbIDListDatabases, ovsdbMethodListDatabases, []any{}, &dbs); err != nil {
		return Result{}, err
	}
	extra := map[string]string{}
	if len(dbs) > 0 {
		extra[extraDatabases] = strings.Join(dbs, ",")
	}

	if !slices.Contains(dbs, ovsdbDatabaseOpenVSwitch) {
		return Result{}, fmt.Errorf("ovsdb does not serve %s database", ovsdbDatabaseOpenVSwitch)
	}
	version, err := ovsdbVersion(enc, dec)
	if err != nil {
		return Result{}, err
	}
	return Result{Version: version, Extra: extra}, nil
}

// ovsdbResponse is a JSON-RPC response from ovsdb-server. Method is set only on
// requests the server interleaves (e.g. an echo keepalive), never on a reply.
type ovsdbResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Method string          `json:"method"`
}

// ovsdbCall sends a JSON-RPC request and decodes the matching reply's result
// into out (when non-nil). It skips any request the server interleaves, matching
// the reply by id.
func ovsdbCall(enc *json.Encoder, dec *json.Decoder, id, method string, params []any, out any) error {
	if err := enc.Encode(map[string]any{ovsdbFieldMethod: method, ovsdbFieldParams: params, ovsdbFieldID: id}); err != nil {
		return probeErr(ProtocolNameOpenVSwitch, stepRequest, err)
	}
	for range ovsdbProbeMaxResponses {
		var resp ovsdbResponse
		if err := dec.Decode(&resp); err != nil {
			return probeErr(ProtocolNameOpenVSwitch, stepResponse, err)
		}
		if resp.Method != "" { // a request from the server, not our reply
			continue
		}
		var gotID string
		_ = json.Unmarshal(resp.ID, &gotID)
		if gotID != id {
			continue
		}
		if len(resp.Error) > 0 && string(resp.Error) != ovsdbJSONNull {
			return fmt.Errorf("ovsdb %s error: %s", method, resp.Error)
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return probeErr(ProtocolNameOpenVSwitch, stepOpenvSwitchDecodeResult, err)
			}
			return nil
		}
		return nil
	}
	return errors.New("no matching ovsdb response")
}

// ovsdbVersion reads ovs_version from the Open_vSwitch table via a transact
// select. A missing row, operation error or malformed optional value means the
// database is not readable enough to satisfy the health check.
func ovsdbVersion(enc *json.Encoder, dec *json.Decoder) (string, error) {
	params := []any{ovsdbDatabaseOpenVSwitch, map[string]any{
		ovsdbFieldOp:      ovsdbOpSelect,
		ovsdbFieldTable:   ovsdbTableOpenVSwitch,
		ovsdbFieldWhere:   []any{},
		ovsdbFieldColumns: []string{ovsdbColumnVersion},
	}}
	var result []struct {
		Error   string `json:"error"`
		Details string `json:"details"`
		Rows    []struct {
			OvsVersion json.RawMessage `json:"ovs_version"`
		} `json:"rows"`
	}
	if err := ovsdbCall(enc, dec, ovsdbIDTransact, ovsdbMethodTransact, params, &result); err != nil {
		return "", err
	}
	if len(result) == 0 {
		return "", errors.New("ovsdb transact returned no operation result")
	}
	operation := result[ovsdbFirstResultIndex]
	if operation.Error != "" {
		detail := operation.Error
		if operation.Details != "" {
			detail += ": " + operation.Details
		}
		return "", fmt.Errorf("ovsdb transact operation failed: %s", detail)
	}
	if len(operation.Rows) == 0 {
		return "", errors.New("ovsdb Open_vSwitch table did not return its root row")
	}
	version, err := decodeOVSDBOptionalString(operation.Rows[ovsdbFirstRowIndex].OvsVersion)
	if err != nil {
		return "", fmt.Errorf("decode ovsdb ovs_version: %w", err)
	}
	return version, nil
}

// decodeOVSDBOptionalString accepts either the scalar encoding used for a
// present optional string or ["set", []] for an absent value (RFC 7047).
func decodeOVSDBOptionalString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var set []json.RawMessage
	if err := json.Unmarshal(raw, &set); err != nil || len(set) != 2 {
		return "", errors.New("expected a string or an empty OVSDB set")
	}
	var tag string
	var values []json.RawMessage
	if json.Unmarshal(set[0], &tag) != nil || tag != "set" || json.Unmarshal(set[1], &values) != nil || len(values) != 0 {
		return "", errors.New("expected a string or an empty OVSDB set")
	}
	return "", nil
}
