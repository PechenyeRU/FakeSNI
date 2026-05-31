# FakeSNI

A tiny Linux helper that keeps your tunnel alive on networks that throttle
"unknown" foreign connections but let *allowed* sites through.

Some national firewalls choke any foreign connection to a crawl unless the very
first packet looks like it's heading to an approved website. If your server sits
behind a CDN, that's enough to kill it. FakeSNI sits in front of your tunnel and
makes the connection *look* approved, so the firewall stops throttling it — while
your traffic still reaches your real server.

You run it next to your tunnel client (sing-box, Xray, …) and point the client
at FakeSNI instead of straight at the server.

## Build

You need [Go](https://go.dev/dl/) (1.25+) and a Linux box.

```sh
git clone https://github.com/PechenyeRU/FakeSNI && cd FakeSNI
go build -o fakesni .
```

That's it — one binary, no other tools to install.

## Configure

Copy `config.json` and edit it:

```json
{
  "LISTEN_HOST": "127.0.0.1",
  "LISTEN_PORT": 40443,
  "CONNECT_IP": "104.21.0.0",
  "CONNECT_PORT": 443,
  "WHITE_SNI": "www.speedtest.net"
}
```

| Field          | What to put |
|----------------|-------------|
| `LISTEN_HOST`  | Where FakeSNI listens. `127.0.0.1` is fine. |
| `LISTEN_PORT`  | Any free port. Your tunnel client will connect here. |
| `CONNECT_IP`   | The IP your tunnel normally connects to (e.g. a Cloudflare IP for your domain). |
| `CONNECT_PORT` | Usually `443`. |
| `WHITE_SNI`    | A site that the network currently lets through. `www.speedtest.net` is a common one. If it stops working, try another allowed site. |

Those five fields are all you need. (A few more knobs exist with sensible
defaults — see `config.go` if you ever want to tune them.)

Every field can also be set with an environment variable named
`FAKESNI_<FIELD>` (e.g. `FAKESNI_WHITE_SNI=www.speedtest.net`). Env vars win
over the file, and if you set all the required ones you don't even need a config
file — convenient for Docker.

## Run

```sh
sudo ./fakesni -config config.json
```

(`sudo` is needed because it works at the raw-packet level.)

Then point your tunnel client at `LISTEN_HOST:LISTEN_PORT`.

### Run with Docker

A prebuilt image is published to GHCR for amd64, arm64 and armv7. Pass the
config via env vars (no file to mount):

```sh
docker run -d --name fakesni \
  --network host --cap-add NET_RAW \
  -e FAKESNI_LISTEN_HOST=127.0.0.1 \
  -e FAKESNI_LISTEN_PORT=40443 \
  -e FAKESNI_CONNECT_IP=104.21.0.0 \
  -e FAKESNI_CONNECT_PORT=443 \
  -e FAKESNI_WHITE_SNI=www.speedtest.net \
  ghcr.io/pechenyeru/fakesni:latest
```

`--network host` and `--cap-add NET_RAW` are required because it works at the
raw-packet level. (If you prefer a file, mount it: `-v "$(pwd)/config.json:/config.json:ro"`.)

### Example: sing-box (VLESS)

Change your VLESS outbound so it dials FakeSNI instead of the server directly —
keep the TLS server name and everything else the same:

```json
{
  "type": "vless",
  "server": "127.0.0.1",
  "server_port": 40443,
  "uuid": "…",
  "tls": { "enabled": true, "server_name": "your-domain.example" },
  "transport": { "type": "ws", "path": "/…", "headers": { "Host": "your-domain.example" } }
}
```

Set FakeSNI's `CONNECT_IP` to the IP your client used to connect to before.

## Notes

- Only the *upload* direction is throttled by these firewalls, so downloads run
  at full speed once a connection is established.
- `WHITE_SNI` is the only thing that "expires": if the network changes which
  sites it allows, swap in another allowed site.
- Linux only.

## Credits

Successor to [patterniha/SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing).
For getting your own connection through censorship. It doesn't touch anyone
else's systems.
