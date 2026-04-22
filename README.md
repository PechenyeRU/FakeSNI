# FakeSNI

A Go port of [patterniha/SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing)
for **Linux**. A TCP proxy that injects a decoy TLS `ClientHello` after the TCP
handshake so on-path inspection sees the decoy SNI while the real server gets
the real connection.

## Features

- Chrome-style `ClientHello` builder (GREASE, full extension set, X25519
  key_share, padded record length) so the fake record's JA3 matches Chrome.
- Decoy SNI pool with `random` / `round_robin` / `sticky_per_connection`
  rotation strategies.
- Three bypass modes, selectable in config:
  - `wrong_seq` — fake segment with a stale seq, server discards it.
  - `low_ttl` — fake segment with low TTL so it expires before the server.
  - `hybrid` — sends both, maximising the chance of a fake being observed
    before the real `ClientHello`.
- Optional TCP fragmentation of the real `ClientHello` into small segments.
- Local JSON stats endpoint on `127.0.0.1:9999`.
- `install.sh` manager: deps, build, systemd, SNI list editor, client-config
  generator (VLESS / Trojan / Shadowsocks link + QR code + 3x-ui outbound JSON).

## Layout

```
main.go         entry point, logging, stats server, signal handling
config.go       JSON config
proxy.go        TCP accept/dial/relay, fragmentation, stats
injector.go     NFQUEUE handler, bypass strategies, raw-socket injection
clienthello.go  Chrome-style ClientHello builder
snipool.go      decoy SNI rotation
stats.go        runtime counters + local HTTP endpoint
system.go       iptables + conntrack sysctl
install.sh      installer / management menu
fakesni.service systemd unit
```

## Build

```sh
go build -o fakesni .
```

## Quick install

```sh
sudo bash install.sh
```

Then use the menu: option 1 installs, option 2 sets the upstream IP/port,
option 4 emits a client config.

## Config

See `config.json` for defaults. Required fields: `LISTEN_HOST`, `LISTEN_PORT`,
`CONNECT_IP`, `CONNECT_PORT`, and either `FAKE_SNI` or a non-empty `SNI_POOL`.

## Requirements

- Linux with `iptables` and `nf_conntrack_netlink`.
- Root (NFQUEUE, raw sockets, conntrack sysctl).
- Go 1.22+ to build.

## Credits

Original technique: [patterniha/SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing).
