# FakeSNI

A fast, clean Go port of [patterniha/SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing)
with full packet-injection support on **Linux**. It runs a TCP proxy that, for each outbound connection, injects
a fake TLS `ClientHello` carrying a decoy SNI right after the TCP handshake.
The fake segment is crafted with a deliberately out-of-window sequence number,
so the remote server discards the payload while any on-path DPI still ingests
the decoy SNI and lets the real traffic through.

## How it works

```
 client ──► fakesni (TCP proxy) ──► real server
                 │
                 │  NFQUEUE observes SYN / SYN-ACK / ACK
                 │  raw socket injects fake ClientHello
                 ▼
       on-path DPI sees "fake SNI"
```

1. The proxy accepts a client and opens a TCP connection to the configured
   remote.
2. Before `connect()` is issued, the connection's 4-tuple is registered with
   the injector so the NFQUEUE callback never races the SYN.
3. The injector captures the SYN / SYN-ACK sequence numbers through NFQUEUE.
4. As soon as the final ACK of the handshake goes out, the injector sends an
   extra TCP segment over a raw socket containing the fake `ClientHello` with
   `seq = syn_seq + 1 - len(payload)` (old, out-of-window).
5. The server replies with a duplicate ACK (it ignores the stale bytes). That
   dup-ACK is the signal the proxy waits on before forwarding any real data.
6. The connection is removed from the tracking map — subsequent packets take
   the NFQUEUE fast path (accept without lookup).

The Linux port uses **NFQUEUE + raw socket** instead of `pydivert/WinDivert`.
eBPF was considered but doesn't fit this workload: the bypass needs async
userspace timing (~1 ms delay between the real ACK and the fake segment) and
one-off packet crafting, which is awkward inside a TC/XDP program. NFQUEUE
gives the same pydivert-style model with mature Go bindings.

## Platform support

- Linux: full bypass mode (`NFQUEUE` + raw socket injection + optional iptables/conntrack setup).
- macOS and other non-Linux systems: plain TCP proxy mode only. The proxy still accepts and forwards traffic, but the fake-SNI bypass is disabled because the Linux packet path is unavailable.

## Requirements

- Linux with `iptables` and `nf_conntrack_netlink` (standard on any modern distro).
- Root (needed for NFQUEUE, raw sockets, and tweaking conntrack sysctl).
- Go 1.22+ to build.

Dependencies (all fetched via `go mod`):

- `github.com/florianl/go-nfqueue`
- `github.com/google/gopacket`
- `golang.org/x/sys`

## Build

```sh
go build -o fakesni .
```

On macOS, the binary builds and runs without root, but it works as a normal TCP proxy and logs a warning that packet bypass is disabled.

## Configuration

Edit `config.json`:

```json
{
  "LISTEN_HOST": "0.0.0.0",
  "LISTEN_PORT": 40443,
  "CONNECT_IP": "188.114.98.0",
  "CONNECT_PORT": 443,
  "FAKE_SNI": "auth.vercel.com",
  "QUEUE_NUM": 100,
  "HANDSHAKE_TIMEOUT_MS": 2000
}
```

| Field                   | Required | Default | Description |
|-------------------------|:--------:|---------|-------------|
| `LISTEN_HOST`           | yes      | —       | Address the proxy listens on. |
| `LISTEN_PORT`           | yes      | —       | Port the proxy listens on. |
| `CONNECT_IP`            | yes      | —       | IPv4 of the real upstream server. |
| `CONNECT_PORT`          | yes      | —       | Port on the upstream server. |
| `FAKE_SNI`              | yes      | —       | Decoy hostname to put in the fake `ClientHello`. |
| `INTERFACE_IP`          | no       | auto    | Source IPv4 to bind outbound sockets to. Auto-detected from the route to `CONNECT_IP` if omitted. |
| `QUEUE_NUM`             | no       | `100`   | NFQUEUE number used for the iptables rule. |
| `HANDSHAKE_TIMEOUT_MS`  | no       | `2000`  | Max time to wait for the fake-ack before giving up on a connection. |
| `NO_IPTABLES_SETUP`     | no       | `false` | If true, `fakesni` will not install/remove iptables rules. You are responsible for pointing traffic into the queue yourself. |
| `NO_CONNTRACK_TWEAK`    | no       | `false` | If true, skip enabling `nf_conntrack_tcp_be_liberal` (see below). |

## Running

```sh
sudo ./fakesni -config config.json
```

Then point your client at `LISTEN_HOST:LISTEN_PORT` (for example, use it as
the upstream of a local HTTP client, or chain it behind another proxy that
forwards raw TCP to this port).

### What `fakesni` touches on the system

By default, at startup it will:

1. Set `/proc/sys/net/netfilter/nf_conntrack_tcp_be_liberal` to `1` so the
   kernel conntrack doesn't drop our out-of-window fake segment. The
   previous value is restored on clean shutdown.
2. Insert two iptables rules redirecting all TCP traffic between
   `INTERFACE_IP` and `CONNECT_IP:CONNECT_PORT` to NFQUEUE:

   ```
   iptables -I OUTPUT -p tcp -s <iface> -d <CONNECT_IP> --dport <CONNECT_PORT> \
            -j NFQUEUE --queue-num <QUEUE_NUM> --queue-bypass
   iptables -I INPUT  -p tcp -s <CONNECT_IP> --sport <CONNECT_PORT> -d <iface> \
            -j NFQUEUE --queue-num <QUEUE_NUM> --queue-bypass
   ```

   Both rules are removed on clean shutdown. If the process is killed with
   `SIGKILL` or crashes, run these by hand to clean up:

   ```sh
   sudo iptables -D OUTPUT -p tcp -s <iface> -d <CONNECT_IP> --dport <port> \
        -j NFQUEUE --queue-num 100 --queue-bypass
   sudo iptables -D INPUT  -p tcp -s <CONNECT_IP> --sport <port> -d <iface> \
        -j NFQUEUE --queue-num 100 --queue-bypass
   ```

Set `NO_IPTABLES_SETUP` / `NO_CONNTRACK_TWEAK` to `true` in the config if you
prefer to manage these yourself.

## Project layout

```
main.go         entry point, config loading, signal handling
config.go       JSON config
proxy.go        TCP accept/dial/relay, coordinates with the injector
injector.go     NFQUEUE handler, raw-socket packet crafting, conn tracking
clienthello.go  minimal TLS ClientHello builder with SNI extension
system.go       iptables + conntrack sysctl setup & cleanup
config.json     sample configuration
```

## Credits

Original Python implementation and bypass technique:
[patterniha/SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing).
