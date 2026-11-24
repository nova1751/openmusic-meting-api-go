# OpenMusic Meting API Go

A Go port of [OpenMusic-Meting-API](https://github.com/qq01-hub/OpenMusic-Meting-API) with native NetEase Cloud Music support. The optional Node.js compatibility service retains the original admin panel and adds QQ Music, Qishui Music, and Kugou Music behind the same `/api` endpoint.

[简体中文](./README.zh-CN.md) · [Releases](https://github.com/nova1751/openmusic-meting-api-go/releases) · [Migration guide (Chinese)](./docs/MIGRATE_NODE_TO_GO.md)

## Deployment modes

| Feature | Standalone Go | Go + Node.js compatibility |
| --- | --- | --- |
| NetEase tracks, playlists, search, artists, radio, cover art, and plain or structured lyrics | Go | Go |
| NetEase playback URLs and Personal FM | Go with local account configuration | Node.js with the existing account pool and supporting services |
| Private NetEase room endpoints | Go | Node.js |
| QQ Music, Qishui Music, and Kugou Music | Requires compatibility mode | Node.js |
| Admin panel, QR-code login, 2FA, cookie validation, monitoring, and backups | Requires compatibility mode | Node.js |
| OpenMusic token and request signing | Go | Uses the existing Node.js tokens, authorization settings, and replay protection |
| Liveness and readiness endpoints | `/livez` and `/readyz` | `/livez` and `/readyz` |

## Quick start

Building from source requires Go 1.26 or later. The container builds and CI use Go 1.27.4.

```sh
git clone https://github.com/nova1751/openmusic-meting-api-go.git
cd openmusic-meting-api-go
```

**POSIX shell with OpenSSL**

```sh
export METING_API_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/meting
```

**PowerShell**

```powershell
$tokenBytes = [byte[]]::new(32)
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($tokenBytes) } finally { $rng.Dispose() }
$env:METING_API_TOKEN = [System.BitConverter]::ToString($tokenBytes).Replace('-', '').ToLowerInvariant()
go run ./cmd/meting
```

The service listens on `127.0.0.1:3000` by default. In another terminal, check readiness with `curl -fsS http://127.0.0.1:3000/readyz` or, in PowerShell, `Invoke-RestMethod http://127.0.0.1:3000/readyz`. Both health endpoints are available without a token.

### Signed API request

By default, `/api` requires a Bearer token and a signed OpenMusic assertion. In a terminal with the same `METING_API_TOKEN`, run this Python 3 example to search NetEase Cloud Music. Sign the exact path and query string sent on the wire.

```sh
python3 - <<'PY'
import base64
import hashlib
import hmac
import json
import os
import secrets
import time
import urllib.parse
import urllib.request

def b64url(value):
    return base64.urlsafe_b64encode(value).decode().rstrip("=")

token = os.environ["METING_API_TOKEN"]
query = urllib.parse.urlencode({
    "server": "netease", "type": "search", "id": "海阔天空", "limit": 3,
})
target = "/api?" + query
claims = {
    "v": 1, "sub": "openmusic:system", "purpose": "system",
    "ts": int(time.time()), "nonce": b64url(secrets.token_bytes(18)),
}
assertion = b64url(json.dumps(claims, separators=(",", ":")).encode())
payload = "\n".join([assertion, "GET", target, hashlib.sha256(b"").hexdigest()])
signature = b64url(hmac.new(token.encode(), payload.encode(), hashlib.sha256).digest())
request = urllib.request.Request("http://127.0.0.1:3000" + target, headers={
    "Authorization": "Bearer " + token,
    "X-OpenMusic-Assertion": assertion,
    "X-OpenMusic-Signature": signature,
})
with urllib.request.urlopen(request, timeout=50) as response:
    print(response.read().decode())
PY
```

To check signing on your local service, set `target = "/test"` in the example. This endpoint returns `{"api":"/api","status":"ok"}` without contacting NetEase.

Keep the token on the server. See the [OpenMusic authorization protocol](https://github.com/qq01-hub/OpenMusic-Meting-API/blob/fb9030ff2b8ab295c213f8542807c1a95e6cf540/docs/OPENMUSIC_AUTH.md) for the signing and request rules, and the [implementation notes (Chinese)](./docs/IMPLEMENTATION.md) for response formats and compatibility details.

### Configuration

For standalone playback and Personal FM, point `NETEASE_ACCOUNTS_FILE` to a JSON file containing credentials for your NetEase accounts. On Unix, restrict the file to mode `0600`. Set `membership` to `free`, `vip`, or `svip` according to the account's actual tier. NetEase determines playback eligibility. At most one account can have `fmPriority` enabled.

```json
[
  {"cookie": "MUSIC_U=<account credential>", "membership": "vip", "fmPriority": true}
]
```

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `3000` | Port used when `LISTEN_ADDR` is unset |
| `LISTEN_ADDR` | `127.0.0.1:$PORT` | Full listen address. The container uses `:3000`. |
| `METING_API_TOKEN` | Required in standalone mode | API token with at least 32 non-whitespace characters |
| `OPENMUSIC_AUTH_ENABLED` | `true` | Require signed assertions in standalone mode. Bearer authentication still applies when set to `false`. |
| `NETEASE_ACCOUNTS_FILE` | Unset | Standalone account JSON file loaded at startup |
| `PUBLIC_BASE_URL` | Request origin | Public base URL used in generated resource links. It can include a path prefix. |
| `REQUEST_TIMEOUT` | `45s` | Timeout for requests handled by Go |
| `NETEASE_DJ_PAGE_DELAY_MS` | `400` | Delay in milliseconds between NetEase radio page requests |
| `LEGACY_URL` | Unset | Private URL of the optional Node.js service |
| `LEGACY_BRIDGE_KEY` | Unset | Shared internal key for the Go service and the Node.js bridge |

## Node.js compatibility deployment

Copy `.env.example` to `.env`, set `LEGACY_BRIDGE_KEY` to a random value from `openssl rand -hex 32`, and set `ADMIN_PASSWORD` if you are starting with a new data directory.

```sh
cp .env.example .env
# Fill in .env before starting.
docker compose up -d --build --wait
curl -fsS http://127.0.0.1:3000/readyz
```

Compose exposes the Go service at `127.0.0.1:3000`. The original admin panel is available at `http://127.0.0.1:3000/admin` by default and still manages tokens, cookies, and authorization settings. Other containers on the same Compose network can use `http://meting:3000`.

For an existing deployment, follow the backup, parallel verification, cutover, and rollback steps in the [migration guide (Chinese)](./docs/MIGRATE_NODE_TO_GO.md).

If you already run the upstream Node.js service, setting only `LEGACY_URL` enables a reverse proxy. To route supported public NetEase requests through Go, run `compat/bridge.mjs` on the Node.js service and set the same `LEGACY_BRIDGE_KEY` on both services. Keep Node.js on a private network reachable by Go.

## Release binaries and container images

[GitHub Releases](https://github.com/nova1751/openmusic-meting-api-go/releases) provides AMD64 and ARM64 binaries for Linux, macOS, and Windows. Download the archive and `SHA256SUMS` into the same directory and run `sha256sum -c --ignore-missing SHA256SUMS` where GNU coreutils is available. Run `meting --version` after extraction to check the version.

Releases also include Go container archives for Linux AMD64 and ARM64. Verify them against `SHA256SUMS.docker` and import your archive with `docker load -i <archive>`. To deploy the matching Go and Node.js images from GHCR, configure `.env` as above and set the release version.

```sh
export METING_VERSION=v0.1.0
docker compose -f compose.yaml -f compose.release.yaml pull
docker compose -f compose.yaml -f compose.release.yaml up -d --no-build --wait
```

If the GHCR packages are private, authenticate before pulling. Public packages support anonymous pulls. See [GitHub Packages visibility and access](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility).

## Development

```sh
go vet ./...
go test -race ./...
go build ./cmd/meting
node --check compat/bridge.mjs
```

The release workflow builds and verifies archives from version tags and publishes both Linux container images. To build the release archives locally, use a Buildx builder that supports the Docker exporter.

```sh
./scripts/build-release.sh v0.1.0
docker buildx create --name meting-release --driver docker-container
BUILDX_BUILDER=meting-release ./scripts/build-docker-release.sh v0.1.0
```

## Credits

The Go port is maintained by [nova1751](https://github.com/nova1751). It follows the API contract and selected NetEase request formats from [OpenMusic-Meting-API at `fb9030f`](https://github.com/qq01-hub/OpenMusic-Meting-API/tree/fb9030ff2b8ab295c213f8542807c1a95e6cf540). The upstream package credits xizeyoupan as its author and declares MIT. This is an independent community project with no affiliation with the upstream project or the supported music services. See [NOTICE.md](./NOTICE.md) for upstream and third-party attribution.

## License

The Go implementation is licensed under [MIT](./LICENSE). The optional Node.js compatibility image includes the upstream Folia-derived QRC implementation, which retains its [AGPL-3.0 license and attribution](./NOTICE.md).
