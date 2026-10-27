# MikroTik Address List Builder

A small Go web service that turns IP2Location country data into importable MikroTik RouterOS scripts.

Select any number of countries, enter an address-list name, and download a `.rsc` file containing one IPv4 CIDR entry per generated range. Every entry includes a comment identifying its country.

## What it does

- Downloads the IP2Location LITE DB3 BIN archive through the official token endpoint.
- Follows the endpoint's cross-origin HTTPS redirect to IP2Location's storage service while blocking HTTPS downgrades and removing the token from cross-origin redirect URLs.
- Refreshes the local database once per 24 hours and reuses an existing fresh copy after a restart.
- Validates and installs updates by swapping the local BIN only after it has downloaded and parsed successfully.
- Merges adjacent IP2Location records for each country and emits the smallest exact CIDR set for every selected range.
- Replaces existing addresses in the target RouterOS list by default, with an option to retain them.
- Serves a self-contained country-selection UI. No browser-side framework or CDN is used.

The local database is stored as `data/ip2location.bin` and is excluded from Git.

## Run locally

Requirements: Go 1.27.3 or newer and an IP2Location download token.

PowerShell:

```powershell
$env:IP2LOCATION_TOKEN = "<your IP2Location token>"
go run .
```

Then open <http://localhost:8080>.

The token endpoint uses the product code `DB3LITEBIN`, which downloads the IPv4 BIN archive. If `IP2LOCATION_TOKEN` is not set, the service still starts and can use an existing local database, but it cannot download or refresh one.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `IP2LOCATION_TOKEN` | none | Token used by `https://www.ip2location.com/download`. Required for downloads. |
| `IP2LOCATION_DATABASE_CODE` | `DB3LITEBIN` | IP2Location product code passed to the download endpoint. |
| `IP2LOCATION_REFRESH_INTERVAL` | `24h` | Minimum time between successful database installs. Minimum accepted value: `1m`. |
| `IP2LOCATION_DOWNLOAD_TIMEOUT` | `10m` | Total timeout for one database download. Minimum accepted value: `30s`. |
| `DATA_DIR` | `./data` | Directory containing `ip2location.bin` and temporary download files. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `PORT` | `8080` | Used to build the listen address when `LISTEN_ADDR` is not set. |
| `TLS_CERT_FILE` | none | PEM certificate chain for native HTTPS. Must be set with `TLS_KEY_FILE`. |
| `TLS_KEY_FILE` | none | PEM private key for native HTTPS. Must be set with `TLS_CERT_FILE`. |

See [.env.example](.env.example) for a template. The service reads environment variables directly and does not load `.env` automatically.

## Docker

```powershell
docker build -t mikrotik-address-list .
docker run --rm --read-only --cap-drop=ALL --security-opt=no-new-privileges `
  --tmpfs /tmp:rw,noexec,nosuid,size=16m `
  -p 8080:8080 -e IP2LOCATION_TOKEN -v mikrotik-data:/data `
  mikrotik-address-list
```

The production image contains only the static Go binary, CA certificates, and an empty data directory; it has no shell or package manager. The container runs as an unprivileged numeric user. Keep the IP2Location token in the container environment and do not pass it into browser code or downloaded scripts.

The builder image is pinned by digest and the Go build has module downloads disabled because the project has no third-party Go dependencies. When updating either Go or the builder image, run the full verification section below and update the digest intentionally.

## Internet-facing deployment

The application is intentionally public: every endpoint is available without sign-in, an API key, an IP allowlist, or per-user quotas.

Internet traffic at an HTTPS reverse proxy is the recommended deployment, with the app reachable only on the private Docker network. Native HTTPS is also supported by setting both TLS file variables. Do not expose the app directly over plain HTTP: generated `.rsc` files are executable RouterOS scripts and must be protected from modification in transit.

Use the edge proxy or your hosting provider for volumetric DDoS protection and broad request-rate limits. The app separately bounds request targets, header size, server timeouts, archive/database sizes, and concurrent script generation so a single oversized request or a burst of expensive downloads cannot consume all server resources.

When TLS terminates at a proxy, forward the public hostname to port `8080` over the private network. The proxy must preserve the app's response headers, including HSTS and the Content Security Policy. Do not trust client-supplied forwarding headers for authentication or access decisions; the service has no such decisions.

## Build for Linux amd64

```powershell
.\build-linux-amd64.ps1
```

The cross-compiled binary is written to `dist/mikrotik-address-list-linux-amd64`. Pass `-OutputDirectory <path>` to write it elsewhere.

## Generated RouterOS script

The download is a complete RouterOS script. A short example:

```routeros
# MikroTik country address list: countries-blocked
# Country: Denmark (DK)
# IP2Location LITE: https://lite.ip2location.com
# Database version: 2026-10-15
/ip/firewall/address-list
remove [find where list="countries-blocked"]
add list="countries-blocked" address=2.16.63.0/24 comment="DK - Denmark"
add list="countries-blocked" address=2.19.173.0/24 comment="DK - Denmark"
```

Address-list names are restricted to letters, numbers, dots, underscores, and hyphens so they can be safely embedded in generated scripts.

## HTTP endpoints

| Endpoint | Purpose |
| --- | --- |
| `GET /` | Country-selection interface. |
| `GET /api/countries` | Available countries and database metadata. |
| `GET /api/status` | Current database state and refresh information. |
| `GET /api/script?name=NAME&country=DK&country=SE` | Download the generated RouterOS script. |
| `GET /healthz` | Health check; returns `503` until a valid database is loaded. |

Add `replace=false` to `/api/script` to retain existing entries in the target list. Traffic to `/api/script` is read-only and does not modify a router.

Each refresh attempt logs its download endpoint, product code, timeout, response status, content type, and the underlying network or redirect error. Redirect chains are limited to 10 hops; redirects may change origin to support IP2Location's storage handoff, but HTTPS cannot be downgraded and the download token is stripped from cross-origin targets. HTTP rejections and responses that are not valid ZIP archives include a short response preview so invalid-token and quota messages are visible. Download query strings are omitted and the token is redacted from all log messages and returned errors. Public status responses remain generic so filesystem paths and credentials are not disclosed.

## Verification

```powershell
go test ./...
go test -race ./...
go vet ./...
govulncheck ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.30.0 -quiet ./...
```

## Data attribution

MikroTik Address List Builder uses the [IP2Location LITE database](https://lite.ip2location.com) for IP geolocation. IP2Location LITE data is subject to the publisher's terms and is not redistributed by this repository.
