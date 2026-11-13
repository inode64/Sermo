package conn

import (
	"encoding/binary"
	"testing"
)

func TestRPCBindDiscoveredTCPProgram(t *testing.T) {
	service := rpcTCPCallTestPort(t, func(call []byte) []byte {
		if len(call) != 40 || binary.BigEndian.Uint32(call[12:]) != 100024 || binary.BigEndian.Uint32(call[16:]) != 1 || binary.BigEndian.Uint32(call[20:]) != 0 {
			t.Errorf("NULL call %x", call)
		}
		return rpcAcceptedReply(binary.BigEndian.Uint32(call), 0)
	})
	mapper := rpcTCPCallTestPort(t, func(call []byte) []byte {
		if len(call) != 56 || binary.BigEndian.Uint32(call[20:]) != 3 || binary.BigEndian.Uint32(call[40:]) != 100024 || binary.BigEndian.Uint32(call[44:]) != 1 || binary.BigEndian.Uint32(call[48:]) != 6 {
			t.Errorf("GETPORT call %x", call)
		}
		reply := rpcAcceptedReply(binary.BigEndian.Uint32(call), 0)
		return binary.BigEndian.AppendUint32(reply, uint32(service))
	})
	result, err := rpcbindProtocol{}.Probe(t.Context(), Config{Host: "127.0.0.1", Port: mapper, Params: map[string]string{ParamKeyRPCProgram: "100024", ParamKeyRPCVersion: "1", ParamKeyTransport: "tcp"}})
	if err != nil || result.Extra[extraRPCStatus] != rpcAcceptNameSuccess || result.Extra["program_version"] != "1" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRPCBindExactVersionAndInvalidPort(t *testing.T) {
	for _, exact := range []bool{false, true} {
		port := rpcAcceptedTCPTestPort(t, rpcAcceptProgMismatch)
		params := map[string]string{ParamKeyTransport: "tcp"}
		if exact {
			params[ParamKeyRPCVersion] = "2"
		}
		_, err := rpcbindProtocol{}.Probe(t.Context(), Config{Host: "127.0.0.1", Port: port, Params: params})
		if (err != nil) != exact {
			t.Errorf("exact=%v: %v", exact, err)
		}
	}
	for _, port := range []uint32{0, 65536} {
		mapper := rpcTCPTestPort(t, func(xid uint32) []byte { return binary.BigEndian.AppendUint32(rpcAcceptedReply(xid, 0), port) })
		_, err := rpcbindProtocol{}.Probe(t.Context(), Config{Host: "127.0.0.1", Port: mapper, Params: map[string]string{ParamKeyRPCProgram: "100024", ParamKeyRPCVersion: "1", ParamKeyTransport: "tcp"}})
		if err == nil {
			t.Errorf("accepted port %d", port)
		}
	}
}

func TestRPCVerifierPadding(t *testing.T) {
	reply := rpcAcceptedReply(7, 0)
	binary.BigEndian.PutUint32(reply[16:], 1)
	reply = append(reply[:20], 1, 0, 0, 0, 0, 0, 0, 0)
	if status, err := parseRPCReply(reply, 7); err != nil || status != rpcAcceptNameSuccess {
		t.Fatalf("%s %v", status, err)
	}
	if _, err := parseRPCReply(reply[:25], 7); err == nil {
		t.Fatal("accepted truncated verifier padding/status")
	}
}

func TestRPCBindTargetValidation(t *testing.T) {
	for _, params := range []map[string]string{{ParamKeyTransport: "icmp"}, {ParamKeyRPCProgram: "0"}, {ParamKeyRPCProgram: "-1"}, {ParamKeyRPCVersion: "4294967296"}, {ParamKeyRPCVersion: ""}} {
		if err := ValidateRPCBindParams(params); err == nil {
			t.Errorf("accepted %v", params)
		}
	}
}
