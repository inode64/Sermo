package conn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func init() { Register(openvswitchProtocol{}, "ovs", "ovsdb", "ovsdb-server") }

// openvswitchProtocol probes Open vSwitch's configuration database server
// (ovsdb-server) over the OVSDB management protocol (RFC 7047), a JSON-RPC
// dialogue. It issues a `list_dbs` request and verifies a JSON-RPC result
// listing the served databases — proof ovsdb-server is up and speaking OVSDB.
// When the `Open_vSwitch` database is present it follows up with a `transact`
// select reading `ovs_version` from the Open_vSwitch table, reported as the
// version. ovsdb-server listens on a Unix socket (set `socket`, commonly
// /run/openvswitch/db.sock) or TCP (default port 6640); `tls` enables SSL.
// No auth.
type openvswitchProtocol struct{}

func (openvswitchProtocol) Name() string       { return "openvswitch" }
func (openvswitchProtocol) DefaultPort() int   { return 6640 }
func (openvswitchProtocol) RequiresUser() bool { return false }

func (openvswitchProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := dialDeadline(ctx, cfg, 6640)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)

	// list_dbs proves the server is up and reports the databases it serves.
	var dbs []string
	if err := ovsdbCall(enc, dec, "0", "list_dbs", []any{}, &dbs); err != nil {
		return Result{}, err
	}
	extra := map[string]string{}
	if len(dbs) > 0 {
		extra["databases"] = strings.Join(dbs, ",")
	}

	// When the Open_vSwitch database is present, read ovs_version for version
	// tracking. A best-effort step: an empty/absent value leaves Version unset.
	version := ""
	for _, db := range dbs {
		if db == "Open_vSwitch" {
			version = ovsdbVersion(enc, dec)
			break
		}
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
	if err := enc.Encode(map[string]any{"method": method, "params": params, "id": id}); err != nil {
		return err
	}
	for i := 0; i < 8; i++ {
		var resp ovsdbResponse
		if err := dec.Decode(&resp); err != nil {
			return err
		}
		if resp.Method != "" { // a request from the server, not our reply
			continue
		}
		var gotID string
		_ = json.Unmarshal(resp.ID, &gotID)
		if gotID != id {
			continue
		}
		if len(resp.Error) > 0 && string(resp.Error) != "null" {
			return fmt.Errorf("ovsdb %s error: %s", method, resp.Error)
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
	return errors.New("no matching ovsdb response")
}

// ovsdbVersion reads ovs_version from the Open_vSwitch table via a transact
// select. It returns "" on any error or when the column is unset (OVSDB renders
// an absent optional column as a set, which does not unmarshal into a string).
func ovsdbVersion(enc *json.Encoder, dec *json.Decoder) string {
	params := []any{"Open_vSwitch", map[string]any{
		"op":      "select",
		"table":   "Open_vSwitch",
		"where":   []any{},
		"columns": []string{"ovs_version"},
	}}
	var result []struct {
		Rows []struct {
			OvsVersion json.RawMessage `json:"ovs_version"`
		} `json:"rows"`
	}
	if err := ovsdbCall(enc, dec, "1", "transact", params, &result); err != nil {
		return ""
	}
	if len(result) == 0 || len(result[0].Rows) == 0 {
		return ""
	}
	var v string
	if json.Unmarshal(result[0].Rows[0].OvsVersion, &v) == nil {
		return v
	}
	return ""
}
