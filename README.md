# discovery-service

A Go implementation of a Beckn **Discovery Service (DS)**: it serves
`POST /discover` requests and answers them asynchronously via
`POST /on_discover` to the caller's callback URL, per Beckn Protocol v2.0.0.

See [CONTEXT.md](CONTEXT.md) for the full architecture, decision log, and
protocol details.

## How it works

### 1. Answering a `/discover` request

A Consumer Node (BAP) asks for products/services; this service matches its
already-indexed catalog data against the request's `message.intent` and
answers either synchronously (`GET /discover`) or asynchronously via a
callback (`POST /discover` → `POST {bapUri}/on_discover`).

```mermaid
sequenceDiagram
    participant CN as Consumer Node (BAP)
    participant DS as discovery-service
    participant Store as CatalogStore (in-memory)

    rect rgba(235, 245, 255, 0.19)
    note over CN,DS: Async path — POST /discover (primary)
    CN->>DS: POST /discover<br/>{context, message.intent}
    DS-->>CN: 200 Ack (signature)
    Note right of DS: Ack is NOT the result —<br/>just "received & queued"
    DS->>Store: Catalogs()
    Store-->>DS: last-known-good []Catalog
    DS->>DS: matchCatalogs(catalogs, intent)
    DS->>CN: POST {bapUri}/on_discover<br/>{context, message.catalogs}
    CN-->>DS: 200 Ack
    end

    rect rgba(240, 255, 240, 0.11)
    note over CN,DS: Sync path — GET /discover (shortcut)
    CN->>DS: GET /discover<br/>{context, message.intent}
    DS->>Store: Catalogs()
    Store-->>DS: last-known-good []Catalog
    DS->>DS: matchCatalogs(catalogs, intent)
    DS-->>CN: 200 {context, message.catalogs}<br/>(result inline, no callback)
    end
```

A malformed `filters` expression is rejected with `400` at the `Ack`/response
stage itself — never discovered later inside a background job with no way
to report it back.

### 2. How a product actually gets indexed, and how intent narrows it down

`CatalogStore` is fed by one of two interchangeable sources
(`CATALOG_SOURCE_MODE`), and every query is narrowed by `matchCatalogs`
against whatever that store currently holds:

```mermaid
flowchart TB
    subgraph PN["Provider Node (BPP)"]
        A1["Dashboard API<br/>GET /api/v1/catalogs"]
        A2[".well-known/dedi.json<br/>+ self-signed CatalogFile / CatalogIndex"]
    end

    subgraph DS["discovery-service"]
        B1["internal/catalogsource<br/>(mode: dashboard — default, live)"]
        B2["internal/dedicrawl<br/>(mode: dedi — verify-then-index)"]
        C["CatalogStore<br/>in-memory snapshot, background-refreshed"]
        D["matchCatalogs(intent)<br/>spatial → textSearch → JSONPath filters → offers"]
    end

    A1 -- "poll every N sec" --> B1
    A2 -- "fetch + verify Ed25519 signature<br/>+ BLAKE2b-512 digest at every hop" --> B2
    B1 --> C
    B2 --> C
    C --> D
    D --> E(["/discover response"])
```

- **`dashboard` mode** (default, what's actually running today): polls a
  BPP's existing internal dashboard API — a stopgap, not a signed/versioned
  Beckn contract (CONTEXT.md D17).
- **`dedi` mode** (opt-in): crawls a Provider Node's self-hosted, Ed25519-signed
  catalog files per `protocol-specifications-v2` §10 — verifying a signature
  or digest at every hop before trusting anything (CONTEXT.md D18). No real
  DeDi registry is reachable yet, so this mode currently only has data to
  serve from a checked-in dev fixture.
- **Matching** (`internal/service/match.go`, CONTEXT.md D19): a catalog
  survives only if it passes every spatial constraint, then its resources
  are narrowed by `textSearch` and/or a JSONPath `filters` expression, and
  its offers are dropped if they only referenced a now-excluded resource. A
  catalog left with nothing at all is excluded from the result.

## Quickstart

```bash
cp .env.example .env
docker compose up --build
```

The service listens on `HOST_PORT` (see `.env`), forwarded to `PORT` inside
the container.

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Run locally without Docker:

```bash
go run ./cmd/discovery
```
