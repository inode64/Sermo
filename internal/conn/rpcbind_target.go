package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

// RPC target selectors use protocol numbers, not daemon-specific command names.
const (
	ParamKeyRPCProgram  = "program"
	ParamKeyRPCVersion  = "version"
	rpcGetPortProcedure = 3
	rpcMappingBytes     = 16
	rpcIPProtocolTCP    = 6
	rpcIPProtocolUDP    = 17
	rpcMaxPort          = 65535
)

type rpcBindTarget struct {
	program, version uint32
	transport        string
	exact            bool
}

// ValidateRPCBindParams checks transport and unsigned ONC RPC target numbers.
func ValidateRPCBindParams(params map[string]string) error {
	_, err := parseRPCBindTarget(params)
	return err
}

func parseRPCBindTarget(params map[string]string) (rpcBindTarget, error) {
	target := rpcBindTarget{program: portmapProg, version: portmapVers, transport: networkUDP}
	if transport, present := params[ParamKeyTransport]; present {
		if transport != networkUDP && transport != networkTCP {
			return target, errors.New("rpcbind transport must be tcp or udp")
		}
		target.transport = transport
	}
	for _, field := range []struct {
		key string
		out *uint32
	}{{ParamKeyRPCProgram, &target.program}, {ParamKeyRPCVersion, &target.version}} {
		if value, present := params[field.key]; present {
			number, err := strconv.ParseUint(value, numericBaseDecimal, 32)
			if err != nil {
				return target, fmt.Errorf("RPC %s must be an unsigned 32-bit integer: %w", field.key, err)
			}
			*field.out = uint32(number)
			target.exact = true
		}
	}
	if target.program == 0 {
		return target, errors.New("RPC program must be positive")
	}
	return target, nil
}

func probeRPCBind(ctx context.Context, cfg Config) (Result, error) {
	target, err := parseRPCBindTarget(cfg.Params)
	if err != nil {
		return Result{}, err
	}
	if target.program != portmapProg {
		port, err := lookupRPCPort(ctx, cfg, target)
		if err != nil {
			return Result{}, err
		}
		cfg.Port = port
	}
	xid := randXID32()
	reply, err := exchangeRPC(ctx, cfg, ProtocolNameRPCBind, defaultPortRPCBind, target.transport, buildRPCNull(xid, target.program, target.version))
	if err != nil {
		return Result{}, err
	}
	if !target.exact {
		return rpcNullResult(reply, xid, ProtocolNameRPCBind, strconv.FormatUint(uint64(target.program), numericBaseDecimal))
	}
	status, _, err := parseRPCReply(reply, xid)
	if err != nil {
		return Result{}, err
	}
	if status != rpcAcceptNameSuccess {
		return Result{}, fmt.Errorf("RPC program %d version %d: %s", target.program, target.version, status)
	}
	return Result{Extra: map[string]string{extraProgram: strconv.FormatUint(uint64(target.program), numericBaseDecimal), extraRPCStatus: status, "program_version": strconv.FormatUint(uint64(target.version), numericBaseDecimal)}}, nil
}

func lookupRPCPort(ctx context.Context, cfg Config, target rpcBindTarget) (int, error) {
	xid := randXID32()
	payload := buildRPCNull(xid, portmapProg, portmapVers)
	binary.BigEndian.PutUint32(payload[rpcProcedureOffset:], rpcGetPortProcedure)
	mapping := make([]byte, rpcMappingBytes)
	binary.BigEndian.PutUint32(mapping, target.program)
	binary.BigEndian.PutUint32(mapping[rpcWordBytes:], target.version)
	protocol := uint32(rpcIPProtocolUDP)
	if target.transport == networkTCP {
		protocol = rpcIPProtocolTCP
	}
	binary.BigEndian.PutUint32(mapping[2*rpcWordBytes:], protocol)
	reply, err := exchangeRPC(ctx, cfg, ProtocolNameRPCBind, defaultPortRPCBind, target.transport, append(payload, mapping...))
	if err != nil {
		return 0, err
	}
	status, body, err := parseRPCReply(reply, xid)
	if err != nil {
		return 0, err
	}
	if status != rpcAcceptNameSuccess {
		return 0, fmt.Errorf("RPC port lookup: %s", status)
	}
	if len(body) != rpcWordBytes {
		return 0, errors.New("RPC port lookup has an invalid result length")
	}
	port := binary.BigEndian.Uint32(body)
	if port == 0 || port > rpcMaxPort {
		return 0, fmt.Errorf("RPC program %d version %d has no valid %s port", target.program, target.version, target.transport)
	}
	return int(port), nil
}
