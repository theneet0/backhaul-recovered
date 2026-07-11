# Transport validation matrix

The recovered runtime accepts 40 transport names and aliases. Every entry below
passed both a real TCP echo relay and a real UDP echo relay with
`accept_udp=true`, using the recovered server and recovered client over
localhost.

| Family | Accepted names | TCP relay | UDP relay | Implementation note |
|---|---|---:|---:|---|
| Original TCP | `tcp` | PASS | PASS | Captured original TCP framing |
| KCP | `kcp` | PASS | PASS | KCP stream over UDP |
| KCP mux | `kcpmux`, `xkcpmux` | PASS | PASS | Recovered mux framing over KCP |
| gRPC | `grpc`, `grpcs` | PASS | PASS | Bidirectional gRPC stream; optional TLS |
| gRPC mux | `grpcmux`, `xgrpcmux`, `grpcsmux`, `xgrpcsmux` | PASS | PASS | Recovered mux framing; optional TLS |
| HTTP CONNECT | `http`, `https` | PASS | PASS | CONNECT tunnel; optional TLS |
| HTTP mux | `httpmux`, `xhttpmux`, `httpsmux`, `xhttpsmux` | PASS | PASS | Recovered mux framing; optional TLS |
| UDP control | `udp` | PASS | PASS | Stream compatibility over connected UDP |
| UDP mux | `udpmux`, `xudpmux` | PASS | PASS | Recovered mux framing over UDP |
| DNS compatibility | `dns` | PASS | PASS | UDP control on a DNS-style endpoint; not a full DNS resolver protocol |
| DNS mux compatibility | `dnsmux`, `xdnsmux` | PASS | PASS | Recovered mux framing over DNS-style UDP control |
| Slipstream | `slipstream`, `slip`, `sstream` | PASS | PASS | Recovered nonce/key stream wrapper |
| Slipstream mux | `slipstreammux`, `slipmux`, `sstreammux` | PASS | PASS | Recovered mux over Slipstream wrapper |
| Raw compatibility | `raw`, `rawsocket`, `socketraw` | PASS | PASS | Mapped to the user-space stream path; not kernel raw-socket equivalence |
| TUN compatibility | `tun` | PASS | PASS | Mapped to the user-space mux path; not kernel TUN equivalence |
| AnyTLS compatibility | `anytls` | PASS | PASS | Recovered TLS stream path |
| TCP mux | `tcpmux`, `xtcpmux` | PASS | PASS | Recovered mux framing over TCP |
| WebSocket | `ws`, `wss` | PASS | PASS | Binary WebSocket framing; optional TLS |
| WebSocket mux | `wsmux`, `xwsmux`, `wssmux` | PASS | PASS | Recovered mux framing; optional TLS |

The Bash manager also generated server and client TOML files for every entry.
All 80 generated files passed the real Go configuration parser and transport
validator.

## Reproduce

```sh
go test ./internal/tunnel \
  -run 'TestEveryTransport(TCP|UDP)Relay' \
  -count=1 -v

go test ./internal/cfg \
  -run TestBashManagerGeneratesEveryTransportConfig \
  -count=1 -v
```

These tests establish internal functionality of the recovered implementation.
They do not claim that compatibility-only transports are wire-equivalent to the
garbled premium binary. Direct original-binary interoperability is confirmed
only for the measured TCP protocol described in `RECOVERY_REPORT.md`.
