# discovery-service — Context & Architecture

Living document. Updated every session with the main decisions so the project
stays on track. Do not delete history here — append/mark superseded instead.

## 1. Purpose

A Go + GORM implementation of a Beckn **Discovery Service (DS)**: it serves
`POST /discover` requests (from a BAP, or from another DS) and answers them
asynchronously via `POST /on_discover` to the caller's callback URL. This
service will be registered as the `/discover` endpoint in a BAP's config for
the **ION network** (Indonesia Open Network).

## 2. Protocol version: Beckn v2, NOT v1 — read this first

This is the single most important framing fact for this project. Beckn
Protocol **v2.0.0** (`protocol-specifications-v2`) renamed and restructured
the discovery contract relative to v1:

| v1 | v2 (what we build) |
|---|---|
| `/search`, `/on_search` | **`/discover`, `/on_discover`** |
| `message.intent.item` | `message.intent.filters` / `.spatial` / `.textSearch` |
| `items` | `resources` |
| `itemAttributes` | `resourceAttributes` |
| snake_case context fields | camelCase (`bapId`, `bapUri`, `transactionId`, `messageId`, `networkId`...) |
| ad-hoc ACK | `Ack` with a required Ed25519 **CounterSignature** proving receipt |

**Do not port v1 field names or shapes from older Beckn examples.** Every
schema in this doc is v2. See §6 for the exact contract.

## 3. Where this fits in the ION network / this org's existing work

Three prior-art repos were scanned to ground this design:

1. **`indonesiaopennetwork/ion-specs`** — the actual spec we implement against.
   `schema/core/v2/api/v2.0.0/beckn.yaml` is a vendored, locked copy of core
   Beckn v2 (`/discover`, `/on_discover` live here). `ion.yaml` layers ION's
   network profile (signing rules, `discoverService: https://discover.ion.id`)
   and ION-specific extensions (`/raise`, `/reconcile`) on top via `$ref` —
   it does **not** redefine `/discover`. **This is our ground-truth contract.**
2. **`beckn/beckn-discovr`** — a mature, already-v2 Java/Spring reference
   implementation of exactly this role (three jobs: catalog-publish-job,
   catalog-discover-job, response-dispatcher; Kafka + Postgres/PostGIS +
   Elasticsearch). We borrow its architecture lessons (§7), not its stack.
3. **`remiges-Tushar/beckn-decentralized-catalog-poc`** — this org's own Go
   POC for **decentralized catalog ingestion** (NFH-014 model): a Provider
   publishes a signed DeDi manifest (`.well-known/dedi.json`) + subscriber
   record + catalog index + catalog files; a DS crawls, verifies Ed25519
   signatures and SHA-256 digests at every hop, and builds a local index. Its
   own README explicitly says: *"We haven't yet exposed `GET /search` or an
   equivalent consumer-facing API."* **That gap is this project.** Style
   lessons taken from it: plain stdlib `net/http` (no framework), constructor
   injection, no field magic, small structs, table-driven tests.

## 4. Decision log

Decisions are numbered and dated; treat them as settled unless explicitly revisited.

**D1 — Ingestion, Milestone 1 (2026-09-04).** We start with a **fixed/mocked
response**: the `/discover` handler validates the request shape per the spec,
returns an `Ack`, and asynchronously delivers a canned, spec-correct
`on_discover` payload to `context.bapUri`. Goal: prove the request/response
contract end-to-end before any real query/storage logic exists. Real catalog
storage (via GORM/Postgres) and real query matching are later milestones.
Decentralized DeDi-based ingestion (reusing the POC's crawler) is a candidate
ingestion path for a *later* milestone, not now.

**D2 — Discover flow is async-only, spec-correct (2026-09-04).** We do not
build beckn-discovr's synchronous `GET /discover` shortcut. Only
`POST /discover` → immediate `Ack` (with CounterSignature once auth lands) →
background worker computes the result → `POST {bapUri}/on_discover`. This
matches the spec's actual contract (`discover → on_discover` in `beckn.yaml`)
and is what real BAP integration requires.

**Confirmed against source (2026-09-08):** cloned
[`github.com/beckn/beckn-discovr`](https://github.com/beckn/beckn-discovr)
to verify D2's claim directly rather than from memory.
`jobs/catalog-discover-job/src/main/java/org/beckn/discover/controller/DiscoveryController.java`
has both:
- `@GetMapping("/discover")` → `discover(...)` — javadoc: *"Synchronous
  discovery endpoint — returns the full `on_discover` response inline."*
  Same `rawBytes`/`headers`/`active`/`validity` params as the POST path,
  routed through the same `handleDiscoverRequest` pipeline
  (authorize → validate → process), but returns `DiscoverResponse`
  (the full result) directly instead of publishing to Kafka.
- `@PostMapping("/discover")` → `discoverPost(...)` — javadoc: *"POST
  endpoint for async Beckn discovery... publishes the request to the
  Kafka request topic and immediately returns an ACK"* — processed later
  by `DiscoveryEventConsumer`, dispatched to the BAP callback by a
  separate response-dispatcher job. This is the one we mirror.
So beckn-discovr's `GET /discover` is a real, deliberate synchronous
shortcut (useful for direct testing / non-Kafka callers) living alongside
the spec-correct async `POST /discover`+`on_discover` flow — not a
misreading on our part. We still only build the POST/async path per D2's
reasoning (real BAP integration on ION uses the callback contract, not a
synchronous shortcut).

**D3 — Auth is stubbed for now, real Ed25519 signing is a later milestone
(2026-09-04).** Beckn v2 requires Ed25519 HTTP Signatures (`Signature` /
`CounterSignature` schemas, BLAKE2b-512 digest, draft-cavage-http-signatures-12
profile) on every request and a signed digest-of-request in the Ack. This is
real crypto work and deserves its own dedicated, well-tested milestone. Until
then, signature verification is bypassed behind a config flag
(`AUTH_ENABLED=false` in dev), and the `Ack.signature` field is emitted as a
clearly-marked placeholder — never presented as a real signature.

**D4 — Datastore: PostgreSQL only, via GORM. Not ClickHouse (2026-09-04).**
GORM was already decided by the user; the remaining question was
Postgres-only vs. ClickHouse vs. beckn-discovr's dual Postgres+Elasticsearch
split.
- ClickHouse is a column-store OLAP engine: excellent for append-only
  analytical aggregation, poor fit here because catalog data needs
  **row-level upserts and per-catalog transactional replace/merge semantics**
  (`FULL` vs `MERGE` publish directives — see beckn-discovr ADR-0010), which
  ClickHouse does not do well (no real transactions, weak `UPDATE`/`DELETE`).
  GORM's ClickHouse driver is also community-maintained and far less
  battle-tested than its Postgres driver.
- Postgres gives us, in one engine: JSONB for `resourceAttributes` (arbitrary
  per-domain schema), `jsonb_path_query`/`@@` for RFC 9535-style JSONPath
  filters, PostGIS for `SpatialConstraint` (S_DWITHIN/S_WITHIN/etc. per
  beckn.yaml), and `pg_trgm`/`tsvector` for `textSearch` — everything the
  Intent object needs, with real transactions for catalog replace.
- **Decision: Postgres only for now.** If text-relevance ranking outgrows
  Postgres full-text later, add a dedicated search engine as a second store
  then (mirrors beckn-discovr ADR-0008's pluggable-engine idea) — don't
  pre-build it.

**D5 — HTTP layer: Go stdlib `net/http`, no framework (2026-09-04).**
Matches this org's existing POC style (constructor injection, minimal deps).
Revisit only if routing needs (path params, middleware chaining) get painful.

**D6 — TDD, docker-compose, reproducibility are non-negotiable process rules
(2026-09-04, restated per explicit user instruction).** Every feature is
built test-first. `docker-compose.yml` must bring up Postgres + the service
with a single `docker compose up --build` for local, reproducible runs. This
CONTEXT.md is updated with the main points of every session so the project
doesn't drift.

**D7 — All ports and configurable values live in `.env`, never hardcoded
(2026-09-04, restated per explicit user instruction).** `internal/config`
loads everything (`PORT`, `DISPATCH_CLIENT_TIMEOUT_SECONDS`,
`DISPATCH_DELIVERY_TIMEOUT_SECONDS`) from env vars with defaults, so the
service runs with zero config but every value is overridable.
`.env.example` documents every variable (checked in); `.env` holds real
local values (git-ignored). `docker-compose.yml` reads `.env` automatically
for `${VAR:-default}` substitution in both `ports:` and `environment:`.
Any new configurable value added later (DB DSN in M2, auth keys in M4, etc.)
must follow this same pattern — add it to `.env.example` with a comment,
read it in `internal/config`, never inline a literal in code or compose.

**Bug found and fixed under D7 (2026-09-04): the binary itself never read
`.env` — only `docker compose` did.** Running `go run ./cmd/discovery` (or
`go run .` from inside `cmd/discovery/`) natively ignored `.env` entirely
and fell back to the hardcoded default `PORT=8080`, which collided with
something already bound to 8080 on a dev machine — silently defeating the
whole point of D7 for anyone not using docker-compose. Fixed with
`internal/config/dotenv.go`: `Load()` now calls `loadDotEnv()` first, which
searches the working directory and up to 8 ancestor directories for a
`.env` file (so it's found whether you run from the repo root or from
`cmd/discovery/`), and calls `os.Setenv` only for keys not already present
in the real environment (so a real env var, or docker-compose's own
`environment:` block, always wins over `.env` — standard dotenv
precedence). Verified live: with the repo's real `.env` set to
`PORT=8087`, `cd cmd/discovery && go run .` now listens on `:8087`, not
`:8080`. Covered by `internal/config/dotenv_test.go` (cwd-in-repo-root,
cwd-in-nested-ancestor, real-env-wins, comments/blank-lines ignored,
no-.env-anywhere-uses-defaults).

**Second gap closed in the same pass, restated per explicit user
instruction ("all components should read .env; default values are fine,
but if `.env` gives a value, use it"): `docker-compose.yml` had its own
duplicate copy of every default.** `PORT: ${PORT:-8080}` etc. meant
Compose *did* prefer an `.env` value when present — it was never actually
broken for the docker path — but it hardcoded a second, independent copy
of every default value (`10`, `100`, `1048576`, ...) that had to be kept
in sync with `internal/config/config.go` by hand, and would silently
diverge the moment one was edited without the other. Fixed: every
app-level variable in `environment:` is now passed through as a bare
`${VAR}` with no fallback — if `.env` sets it, that value is used; if not,
Compose sets it to an empty string in the container, and `internal/config`
already treats an empty env var as "unset" and applies its own default.
`internal/config/config.go` is now the *only* place any default value for
an app setting is written down. The two exceptions, kept as Compose-side
fallbacks deliberately: `ports:` (Compose syntax requires a literal port
number) and the new `STOP_GRACE_PERIOD` var (a pure Docker directive the
Go binary never reads at all, so there's no app-side default for it to
defer to). Verified via `docker compose config` (real `.env` value
`PORT=8087` flows through to both the container env and the `ports:`
target with no stale `8080` anywhere) and a full container run.

**D8 — Every external component sits behind an interface owned by the
business-logic layer; concrete implementations are swappable adapters
(2026-09-04, restated per explicit user instruction).** This is the
**Ports and Adapters** (Hexagonal Architecture) pattern, also called
**Dependency Inversion**: `internal/service` never imports a concrete
database driver, HTTP client library, or queue client directly — it
declares the interface (*port*) it needs, and a separate package provides
the concrete implementation (*adapter*). The example that prompted this
rule: **if the catalog store were ClickHouse today, it must be just as easy
to swap to Postgres tomorrow** — achieved by never letting `service` see
anything but a `CatalogStore` interface, with `internal/store/clickhouse`
or `internal/store/postgres` as interchangeable adapters (M2+; no store
exists yet). We are already applying this pattern today, one milestone
before there's a database at all:
- `service.OnDiscoverDispatcher` (port) ← `dispatch.HTTPDispatcher` (adapter).
  A future queued/retrying dispatcher is a new adapter, zero changes to `service`.
- `service.AsyncRunner` (port) ← `worker.Pool` (adapter). A future
  broker-backed runner is a new adapter, zero changes to `service`.
- `handlers.DiscoverHandler` depends only on `*service.DiscoverService`
  (a concrete type, but one with zero I/O of its own — all I/O is behind
  the two ports above), so the HTTP layer never touches infrastructure
  directly either.

**D9 — Concurrency: use goroutines fully, but every defect gets found and
fixed, not just "made to work" (2026-09-04, restated per explicit user
instruction).** A senior-Go-developer pass over the M1 async-dispatch path
found and fixed the following concrete defects (see §10 for the full
write-up):
1. Unbounded goroutine fan-out per request → bounded `worker.Pool`.
2. Unrecovered panic in a background job crashes the whole process →
   per-job `recover()` inside the pool.
3. No graceful shutdown → `main.go` traps SIGINT/SIGTERM, drains the HTTP
   server and the worker pool within a configurable deadline.
4. A first draft of `Pool.Shutdown` blocked past its own deadline waiting
   on a job that doesn't observe `ctx` cancellation — verified as a real
   hang via a test, then fixed to honor the deadline (Go cannot force-kill
   a goroutine; a shutdown deadline must not become unbounded because of
   that).
5. `http.Server` had no timeouts (slowloris-class resource exhaustion) →
   explicit `ReadHeaderTimeout`/`ReadTimeout`/`WriteTimeout`/`IdleTimeout`.
6. Unbounded request body read → `http.MaxBytesReader`, size configurable.

**D10 — Structured logging via `logharbour` (Remiges), with an .env-configurable
level, and every HTTP hit visible by default (2026-09-04, per explicit
user instruction: "it should be observable... use logharbour logging from
remiges").** Before this, `handlers.DiscoverHandler` logged nothing at all
on the success path — only failures (via `log/slog`) ever produced
output, which is why a real request produced no visible console line.
Fixed:
- Replaced every `log/slog` call across `worker`, `service`, and `main.go`
  with `github.com/remiges-tech/logharbour/logharbour` — module
  `github.com/remiges-tech/logharbour`, package `.../logharbour`
  (`NewLoggerContext`, `NewLoggerWithFallback`, `NewFallbackWriter`,
  `LogActivity`/`LogDebug`, priority levels `Debug2 < Debug1 < Debug0 <
  Info < Warn < Err < Crit < Sec`). `internal/logging` wraps it with
  `ParsePriority(level string)` (maps a `LOG_LEVEL` string to a
  `logharbour.LogPriority`) and `IsDebugLevel` (logharbour requires BOTH a
  low-enough priority AND a separate `SetDebugMode(true)` on the
  `LoggerContext` before `LogDebug` emits anything — `IsDebugLevel` decides
  when to flip that flag based on `LOG_LEVEL`).
- **New `LOG_LEVEL` var in `.env`** (default `info`; the real repo `.env`
  is now set to `debug` per this request). Accepted values documented in
  `.env.example`: `debug2`/`debug1`/`debug0` (or `debug`)/`info`/`warn`/
  `err` (or `error`)/`crit`/`sec`.
- **`handlers.LoggingMiddleware`** wraps the entire `http.ServeMux` in
  `main.go` (every route, including `/healthz`) and logs
  method/path/remoteAddr/status/duration at **Info** for every single
  hit — deliberately not gated behind the debug tier, so hits are visible
  at the default log level, not only when `LOG_LEVEL=debug`.
  `DiscoverHandler` additionally logs Beckn-specific context
  (transactionId/messageId/bapUri) on accept/reject at Info/Warn, and the
  *full* parsed request body at Debug0 — that extra layer of detail is
  what `LOG_LEVEL=debug` actually unlocks.
  `service.DiscoverService.ProcessAsync` logs dispatch-scheduled/
  delivered/failed/dropped-at-capacity, replacing its old silent-except-
  on-error `slog` calls.
- **Verified live**, not just by test: ran `go run .` from `cmd/discovery`
  with the real `.env` (`LOG_LEVEL=debug`), hit `/healthz` then a real
  `/discover` request, and confirmed the console printed: the startup
  line, an `http request` line for `/healthz`, a Debug0 `discover request
  parsed` line with the full request body, an Info `discover request
  accepted` line, `on_discover dispatch scheduled`, an `http request` line
  for `/discover` itself, and `on_discover delivered` — then a live
  SIGTERM producing `shutdown signal received` → `discovery-service
  stopped`, all as structured JSON through the same logger.
- **Known trade-off, stated plainly rather than hidden**: `logharbour`'s
  Go package is monolithic — `Logger`/`LogActivity` and its Kafka
  consumer/producer and Elasticsearch-backed storage code all live in one
  package (`.../logharbour`), so importing the former pulls in the
  latter's dependency tree (IBM Sarama/Kafka, the Elasticsearch v8 client,
  GeoIP, OpenTelemetry) even though none of it is used here — we only use
  `NewLoggerWithFallback` writing structured JSON to stdout. `go.sum` grew
  from 0 to ~139 lines for what is, functionally, still just a console
  logger. This was an explicit, named request ("use logharbour logging
  from remiges"), so it's accepted as-is rather than substituted for a
  lighter option — flagging it here so it's a documented decision, not a
  surprise in a future dependency audit.

**D11 — The fixed M1 demo catalog is real, varied product data, stored as
its own JSON file, not inline Go structs (2026-09-04, per explicit user
instruction: "add a few actual products... store this static product json
as a separate file").** `internal/service/data/catalog.json` holds one
catalog with 3 realistic products (a laptop, earbuds, a smart watch — each
with a brand, category, and price in `resourceAttributes`) and 3 matching
offers. `internal/service/response.go` embeds it via `//go:embed` and
parses it **once at package init** (`mustParseFixedCatalogs`) — a
malformed JSON file panics at process startup with a clear message,
rather than surfacing as a confusing failure on the first `/discover`
request. `BuildFixedOnDiscover` returns a **fresh copy per call** (with
`BppID`/`BppURI` stamped from the request context) rather than handing out
the shared package-level slice directly — verified by
`TestBuildFixedOnDiscover_DoesNotMutateSharedCatalogDataAcrossCalls`, since
this data is read by concurrent `/discover` handlers.
- **Why embed instead of reading the file from disk at runtime**: the
  same working-directory fragility that broke `.env` loading (see the D7
  bugfix above) would apply here too — a relative path to
  `data/catalog.json` resolves differently depending on whether the
  process is run from the repo root, from `cmd/discovery/`, or from a
  container's `WORKDIR`. `go:embed` bakes the file into the binary at
  build time, so it's still a genuinely separate, independently-editable
  JSON file (this was the actual ask), just not re-read from disk without
  a rebuild. Real, editable-without-a-rebuild catalog data is what M2's
  GORM/Postgres store is for.
- Verified live end-to-end (not just unit tests): started the service,
  sent a real `/discover` request, and confirmed the `on_discover`
  callback carried all 3 products with their brands/prices and all 3
  offers correctly referencing their resources.
- Deferred at the time, since resolved by D13 below: a proper
  `Consideration` chain for price is now modeled (`Offer.Considerations`),
  and `data/catalog.json` was replaced with a real-world catalog that
  uses it correctly instead of the informal `resourceAttributes.price`
  this milestone started with.

**D12 — Bug found and fixed via spec re-audit (2026-09-04, prompted by
explicit user question: "are we sending the response as needed and as
followed in ION documentation?"): `Ack.signature` was serialized as a
nested JSON object; the spec requires it to be a plain string.**
`beckn.yaml#/components/schemas/Signature` is declared `type: string`
with a specific pattern (`Signature keyId="{subscriberId}|{uniqueKeyId}|{algorithm}",algorithm="...",created="...",expires="...",headers="...",signature="..."`,
draft-cavage-http-signatures-12 profile), and `CounterSignature` —
which is what `Ack.signature` actually is — is `allOf: [Signature]`, so
it's a string too. Our original `beckn.Signature` Go type was a struct
with `keyId`/`algorithm`/`created`/... fields, which serialized as
`"signature": {"keyId": ..., ...}` — wrong shape, and would fail schema
validation against `beckn.yaml` on any real BAP/network side. **This bug
was in the original M1 implementation from the start, and the CONTEXT.md
§6 example I wrote at the time repeated the same mistake** (worth noting:
I had read the correct spec text earlier in this project and still got
the Go modeling wrong — a reminder that "I read the spec" doesn't
guarantee "I modeled the spec correctly," and re-auditing against the
actual schema text periodically catches this kind of drift).

Fixed:
- `beckn.Signature` is now `type Signature string`, with
  `beckn.FormatSignature(subscriberID, uniqueKeyID, algorithm string, created, expires int64, headers, signatureValue string) Signature`
  assembling the exact pattern string.
- `service.PlaceholderSignature()` now calls `FormatSignature` with a
  real (non-cryptographic) `created`/`expires` Unix-timestamp pair (the
  pattern requires `\d+`, so an empty string — which the old code used —
  would have failed the regex too) and a base64-encoded placeholder value
  (the pattern's `signature="..."` field is base64-alphabet-only; the old
  literal `"UNSIGNED-PLACEHOLDER"` contains a hyphen, which is outside
  `[A-Za-z0-9+/]` and would also have failed the regex).
- **Guardrail tests added at both layers**: `internal/beckn/signature_test.go`
  matches `FormatSignature`'s output against the exact spec regex, and
  `internal/service/discover_test.go` does the same for
  `PlaceholderSignature()` — so a future change that breaks the pattern
  fails a test immediately instead of silently shipping a malformed Ack.
  `internal/handlers/discover_test.go` also asserts the raw wire-level
  `signature` field decodes as a JSON string, not an object.
- Verified live: restarted the running service and confirmed the actual
  HTTP response body now reads
  `"signature":"Signature keyId=\"unsigned|placeholder|none\",algorithm=\"none\",created=\"...\",expires=\"...\",headers=\"(created) (expires) digest\",signature=\"...\""`
  — a single string, matching the pattern exactly.
- **This was the one substantive gap found in the full re-audit.**
  Everything else checked against `beckn.yaml` in this pass was already
  correct: the `/discover` request/`Ack`/`on_discover` envelope shapes
  (no extra wrapper, `message.catalogs` nesting), `Error{errorCode,
  errorMessage}` on NACK, `context.action` values, and the request being
  fully echoed into the `on_discover` context. One minor thing flagged
  but *not* changed (not a correctness bug, just an unmodeled field):
  `DiscoverAction` in the spec also permits an optional flat sibling
  `message.textSearch` alongside `message.intent.textSearch`; our
  `beckn.DiscoverMessage` only has `Intent` — low priority since real
  intent/query validation is already a known, separately-tracked gap
  (see M1's "known simplification" note above).

**D13 — Catalog data upgraded to a real observed ION network response;
fixed a real "own BPP identity" gap it revealed (2026-09-04, per a real
`/discover`→`on_discover` exchange the user captured against a live
`ds-fabric.ion.id` / `discover.infra.ion.id` deployment).** Two changes:

1. **`beckn` types extended** to cover fields the real response uses that
   we didn't model yet: `Descriptor.mediaFile` (`[]MediaFile{label,
   mimeType, uri}`), and on `Offer`: `provider` (an offer can carry its
   own `Provider`, distinct from the resource-level one — e.g. a
   different pickup location), `considerations` (`[]Consideration{id,
   status, considerationAttributes}` — this is the *actually spec-correct*
   home for price/value data, which D11 had flagged as deferred and
   informally left in `resourceAttributes`), and `offerAttributes` (open
   bag for return/payment/serviceability policies).
2. **`internal/service/data/catalog.json` replaced with that exact real
   payload** (two `CatPub Coffee Catalog` entries, verbatim field values)
   — not synthetic data — per explicit instruction to store and serve
   "such a response."
3. **Real bug this data exposed**: the captured request had **no**
   `context.bppId`/`bppUri` at all (the BAP was calling a network-wide
   discover service, not addressing one specific BPP), yet the real
   response correctly filled in the responding service's own identity
   (`bppId: "ds-fabric.ion.id"`). Our code previously did
   `respCtx := reqCtx` and left `BppID`/`BppURI` however the request had
   them — empty stayed empty. Separately, the real response's **second**
   catalog carries its *own* distinct `bppId: "obs-seller.loca.lt"` (a
   federated/fan-out result from a different BPP) that must never be
   overwritten. Fixed:
   - New config: `BPP_ID`/`BPP_URI` (this service's own registered
     identity; blank by default, see `.env.example`). Threaded through
     `service.NewDiscoverService` → `BuildFixedOnDiscover(reqCtx,
     ownBppID, ownBppURI)`.
   - `BuildFixedOnDiscover` now fills `respCtx.BppID`/`BppURI` from the
     configured own identity **only when the request left them empty**
     (a request that already addresses a specific BPP is echoed
     unchanged — never overwritten).
   - Each catalog inherits that same rule independently: a catalog with
     no `bppId` of its own in `data/catalog.json` inherits the response
     context's identity; a catalog that already declares one (like the
     second CatPub catalog) keeps it untouched.
   - Tests added for exactly these three cases (request has no bpp
     identity / request already has one / mixed per-catalog inheritance
     within one response), plus the pre-existing
     no-cross-request-mutation test extended to cover it.
   - **Verified live**: sent a request shaped like the real captured one
     (no `context.bppId`) and confirmed the callback shows catalog 1
     inheriting `ds-fabric.ion.id` while catalog 2 keeps its own
     `obs-seller.loca.lt` — matching the real reference exactly.

**D14 — Real delivery failure debugged end-to-end against a live
`onix-bap` test container; two distinct, unrelated causes found and
fixed in sequence, one per this-side bug and one config-driven (2026-09-04,
user reported `on_discover delivery failed` with a DNS error, later a 404).**
1. **DNS resolution failure (`server misbehaving`)** — `docker compose ps`
   showed the service wasn't actually running in its container; the
   failing call came from a native `go run` process, which can never
   resolve `onix-bap` (a hostname that only exists in Docker's per-network
   embedded DNS). Fixed by running via `docker compose up`, attached to
   the `beckn-app_beckn` network `docker-compose.yml` already referenced
   — confirmed `onix-bap` resolves once actually on that network.
2. **404 on `POST {bapUri}/on_discover`** — not a bug in our delivery
   logic (`bapURI + "/on_discover"` is exactly per spec, since
   `context.bapUri` is supposed to already be the full registered
   subscriber base URL). `docker logs onix-bap` showed its ONIX
   `bapTxnReceiver` plugin is actually exposed under `/bap/receiver/`,
   meaning the bapUri this BAP's registry entry was configured with is
   incomplete for its own routing — a config problem on that BAP's side,
   confirmed by testing `POST /bap/receiver/on_discover` directly (`400`,
   a schema-validation rejection — proving the route exists there).
   Rather than hardcode a specific vendor's path prefix (which would be
   wrong for any other, correctly-registered BAP), added a **configurable
   override**: `ON_DISCOVER_PATH` (`config.Config.OnDiscoverPath`,
   `constants.OnDiscoverPath` as the spec-correct default), threaded into
   `dispatch.NewHTTPDispatcher`. The real `.env` sets it to
   `/bap/receiver/on_discover` for this sandbox specifically, with a
   comment explaining it's a local quirk, not a spec requirement.
   Verified live: error changed from `404` to `401` after the fix.

**D15 — Real Ed25519 request signing for outbound `on_discover` delivery
implemented (2026-09-04, the `401 AUT_SIGNATURE_MISSING` that D14's fix
revealed).** Beckn v2 requires every request to carry a signed
`Authorization` header (draft-cavage-http-signatures-12, same string
format as `Ack.signature`/`CounterSignature` — see D12). This had been
stubbed since M1 (D3). New `internal/signing` package:
- `KeyPair`/`GenerateKeyPair`/`LoadKeyPair` (Ed25519, `crypto/ed25519`).
- `Signer.Sign(body)`: BLAKE2b-512-digests the body (`golang.org/x/crypto/blake2b`
  — already a transitive dependency via `logharbour`, no new heavy import),
  builds the signing string exactly as `beckn.yaml` specifies
  (`(created): {ts}\n(expires): {ts}\ndigest: BLAKE-512={base64Digest}`),
  signs it with Ed25519, and returns a `beckn.Signature` via
  `beckn.FormatSignature`. Round-trip-verified in tests
  (`ed25519.Verify` against the reconstructed signing string).
- `LoadOrGenerateKeyPair`: loads a persistent key from
  `SIGNING_PRIVATE_KEY_BASE64` if set, otherwise generates a fresh
  **ephemeral** one every process start and logs its public key. An
  ephemeral key is enough to stop a receiver rejecting a request for
  *having no signature at all*, but nothing can ever cryptographically
  verify it (it's not registered anywhere) — this is a stated, logged
  limitation, not a claim of real security.
- `dispatch.HTTPDispatcher` takes an optional `RequestSigner`; when set,
  every outbound request gets a real `Authorization` header.
  `main.go`'s `buildSigner` only constructs one when `BPP_ID` is
  configured (signing without a claimed identity is meaningless), and
  logs a warning when falling back to an ephemeral key.
- **Verified live against the real `onix-bap` container**: the error
  progressed from `401 AUT_SIGNATURE_MISSING` → `500`, and `onix-bap`'s
  own logs showed exactly why — it received our signed request, correctly
  extracted `keyId="ds-fabric.ion.id|key-1|ed25519"`, and queried the
  **real, live DeDi registry** (`https://fabric.nfh.global/registry/dedi/lookup/...`)
  to fetch that subscriber's public key to verify against — getting back
  `404 record not found`. This confirms the entire signing pipeline is
  now spec-correct end to end; the only remaining blocker is that nothing
  is actually registered under that identity in a real registry, which
  no amount of code can fix.
- **Important callout, not yet resolved**: `BPP_ID`/`BPP_URI` in the real
  `.env` are still set to `ds-fabric.ion.id` / `https://discover.infra.ion.id`
  — borrowed from the D13 example purely to test response *shape*. Now
  that outbound requests are **actually signed** claiming to be that
  subscriber, continuing to use it is presenting as a network identity
  this deployment doesn't own or control. `.env` now carries an explicit
  warning; the value has been left as-is (not silently changed) pending
  the user providing their own registered identity, or explicitly
  deciding to leave `BPP_ID` blank (which sends unsigned requests with no
  `bppId`/`bppUri`, rather than claiming someone else's).
- **Not built**: inbound signature *verification* (authenticating an
  incoming `/discover` request's own `Authorization` header) and
  registry-based callback URL resolution (the SSRF-safety gap noted in
  §7/D14) are both still open — `D3`'s full scope isn't closed by this,
  only the outbound-signing half of it.

**D16 — Added a synchronous `GET /discover` shortcut alongside the async
`POST /discover` (2026-09-08), reversing part of D2, per explicit user
instruction that a real caller (part of ION / a BAP) actually expects a
synchronous GET.** D2 stood on the premise that only the spec's async
contract matters; this is a real, external requirement, not a testing
convenience, so it's a deliberate exception rather than a re-litigation
of D2's reasoning — the async POST path is unchanged and remains the
default/primary path.
- `service.DiscoverService.ValidateSync` — a second, narrower validator
  than `Validate`: still requires `context.transactionId`/`messageId`,
  but deliberately does **not** require `context.bapUri` — a synchronous
  caller gets the result back inline in the HTTP response, so there is no
  callback to address.
- `service.DiscoverService.BuildSync` — builds and returns the
  `on_discover` payload directly (reuses `BuildFixedOnDiscover`, same
  fixed-catalog data as the async path per D11/D13); unlike
  `ProcessAsync`, nothing touches `OnDiscoverDispatcher` or `AsyncRunner`
  at all — it's a pure, synchronous build-and-return.
- `handlers.DiscoverHandler.ServeSync` — the GET handler. Shares body
  decoding with `ServeHTTP` (POST) via a new private
  `decodeAndValidate(w, r, validate)` helper parameterized by which
  validator to run, so the two paths don't duplicate the JSON-decode/
  size-limit/logging logic, only the field-requirement rule differs.
- Wired in `main.go` as `mux.HandleFunc("GET /discover",
  discoverHandler.ServeSync)`, alongside the existing
  `mux.Handle("POST /discover", discoverHandler)` — Go's `net/http`
  method-based mux patterns (1.22+; this module targets `go 1.25.0`)
  route them independently, no shared state issue.
- Response shape: `beckn.OnDiscoverRequest` (the same shape delivered to
  `context.bapUri` on the async path), `200 OK`, written directly as the
  GET response body — not wrapped in an `Ack`.
- Confirmed this really does mirror `beckn-discovr`'s own `GET /discover`
  (see D2's addendum above): same request body/headers, same
  authorize→validate→process pipeline, difference is only sync-inline
  vs. async-Kafka-then-dispatch.
- Tests added: `internal/service/discover_test.go`
  (`TestValidateSync_DoesNotRequireBapUri`,
  `TestValidateSync_StillRequiresTransactionIDAndMessageID`,
  `TestBuildSync_ReturnsOnDiscoverPayloadDirectly`) and
  `internal/handlers/discover_test.go`
  (`TestDiscoverHandler_ServeSync_ValidRequest_ReturnsOnDiscoverInline`,
  `TestDiscoverHandler_ServeSync_MissingBapUri_StillAccepted`,
  `TestDiscoverHandler_ServeSync_MissingTransactionID_ReturnsNack400`).
  All 77 tests across 11 packages pass under `-race`.
- Known limitation carried over unchanged from the async path: this
  still returns the fixed/mocked M1 catalog (D11/D13), not a real query
  match — real query logic (M3) will apply equally to both `/discover`
  entry points once it lands, since both call into the same
  `BuildFixedOnDiscover`/future `CatalogStore`-backed builder.

**D17 — Real catalog data via polling BPP instances' existing dashboard
API (2026-09-15), after ruling out both catalog-storage ownership and
filesystem-path crawling.** This decision has three parts, each reached
after a wrong assumption was corrected:

1. **Scope correction: discovery-service does not own catalog
   storage/ingestion.** An earlier session mistakenly scoped M2 as
   building beckn-discovr's `catalog-publish-job` equivalent
   (`/catalog/push`, `/catalog/on_pull`, FULL/MERGE replace semantics)
   *inside* discovery-service. Corrected per explicit user pushback:
   `catalog-discover-job` (what this project mirrors) only ever *reads*
   a catalog store in the reference architecture — it never owns
   publishing/storage, which is a genuinely separate job even in
   beckn-discovr itself. Confirmed by inspecting the real repo:
   `jobs/catalog-discover-job/.../controller/` contains only
   `DiscoveryController` and `HealthController` — no catalog-store
   endpoint exists there at all.
2. **Filesystem-path "crawling" was ruled out, not because of scope, but
   because it cannot survive the deployment target.** The user proposed
   an `.env`-configured array of local file paths a BPP publishes
   catalog data to, for discovery-service to read directly off disk.
   Investigating the actual `bpp-application` reference repo
   (`internal/catalog/publish_service.go`) showed it does not write
   catalog files to any local path at all — it stores catalogs in its
   own Postgres DB and (optionally) forwards them via an outbound HTTP
   POST to a configured `CDS_PUBLISH_URL`, a push model, not a
   file-crawl model. Separately, and decisively: this service is
   deployed via Docker first, then GCP Cloud Run — Cloud Run services
   share no filesystem with each other at all, so a local-path
   mechanism could never work in production regardless of scope.
3. **Chosen mechanism: poll bpp-application's existing dashboard API.**
   The user's hard constraint: no code or config access to the BPP
   side — whatever ingestion mechanism exists must be built entirely
   within discovery-service. `bpp-application` already exposes
   unauthenticated HTTP GET endpoints over its stored catalog data with
   no code change needed:
   `GET /api/v1/catalogs` (paginated list, `internal/server/server.go`
   line 168) and `GET /api/v1/catalogs/{id}` (full detail — catalog +
   resources incl. `resourceAttributes`/media + offers incl.
   `considerationAttributes` —
   `internal/dashboardsvc/catalog_handlers.go` `HandleGetCatalog`).
   `GET /api/v1/catalogs/{id}/resources` was checked and rejected as a
   data source — it returns only `{id, name}` pairs, a UI picker list,
   not enough to build a real catalog.
   - New package `internal/catalogsource`: `HTTPSource` polls each
     configured base URL's list+detail endpoints and maps the
     dashboard-shaped JSON into `beckn.Catalog`/`Resource`/`Offer`
     (`map.go`); `Store` caches the result and exposes it via
     `Catalogs()` with zero I/O on the read path — a background
     goroutine (`Start`) refreshes on a timer, and a failed refresh
     (all sources unreachable) keeps serving the last-known-good
     snapshot rather than going empty.
   - New config: `CATALOG_SOURCE_URLS` (comma-separated BPP base URLs;
     empty by default), `CATALOG_REFRESH_INTERVAL_SECONDS` (default 60),
     `CATALOG_FETCH_TIMEOUT_SECONDS` (default 10).
   - `service.CatalogStore` is the new port (`Catalogs() []beckn.Catalog`)
     `DiscoverService` depends on instead of the hardcoded embedded
     catalog — see D8. `service.EmbeddedCatalogStore` (wrapping the
     original M1 `data/catalog.json`) is the zero-config default when
     `CATALOG_SOURCE_URLS` is unset, so M1 behavior is unchanged out of
     the box. `BuildFixedOnDiscover` (M1) is now a thin wrapper around
     the new `BuildOnDiscover(reqCtx, ownBppID, ownBppURI, catalogs)`,
     which is what actually builds the response from whichever catalogs
     it's given.
   - `main.go`'s `buildCatalogStore` does one synchronous `Refresh` at
     startup (bounded by `CatalogFetchTimeout`) before serving traffic,
     then starts the background polling goroutine, cancelled during
     graceful shutdown alongside the HTTP server and worker pool.
   - **Explicitly named as a stopgap, not a stable integration**: these
     are `bpp-application`'s internal dashboard/admin endpoints, not a
     published Beckn contract — no authentication, no versioning
     guarantee, could change without notice. Accepted because it's the
     only network-reachable, no-BPP-code-change option available today.
   - **Known data gap carried forward, not fixed here**: the dashboard
     API's catalog detail response has no `provider.availableAt`
     (geo/address) field — only `providerId`/`providerName` — so
     catalogs sourced this way can never satisfy a spatial `Intent`
     (M3) until/unless a source provides that data another way.
   - Tests: `internal/catalogsource/fetch_test.go` (dashboard→beckn
     field mapping including media files and consideration attributes,
     pagination across a 101-catalog list, one-source-failure isolation,
     all-sources-fail signaling) and `store_test.go` (populate-on-
     refresh, stale-data-survives-total-failure, copy-not-shared-state,
     periodic `Start`/cancellation); `internal/service/response_test.go`
     gained `BuildOnDiscover`/`EmbeddedCatalogStore` coverage. All
     existing M1/D16 tests pass unmodified (89 tests total across 12
     packages, `-race` clean).

**D18 — `internal/dedicrawl` wired in as an opt-in alternative to
`catalogsource`, behind `CATALOG_SOURCE_MODE` (2026-09-16).** A separate,
already-built package implementing `protocol-specifications-v2`'s
`Catalog_Publishing_and_Discovery.md` §10 decentralized model existed in the
repo (Registry manifest → subscriber record → catalog index → signed
baseline/change files, Ed25519 + BLAKE2b-512 + JCS verified at every hop)
but was never reachable from `cmd/discovery/main.go` — a from-scratch
implementation of the ingestion side of the spec, distinct from and not a
port of `catalogsource` (D17) or beckn-discovr. Read directly against the
raw `beckn.yaml`/RFC text (not just this repo's own docs) to confirm two
things: `/discover`/`/on_discover` themselves already match `beckn.yaml`
field-for-field, and `catalogsource` is genuinely live in production
(verified: the running container was actively polling `bpp:8080` and serving
4 real catalogs) — so nothing about this work could risk that path.

- **Config switch, not a replacement.** New `CATALOG_SOURCE_MODE`
  (`"dashboard"` default / `"dedi"`) in `internal/config/config.go`;
  `cmd/discovery/main.go`'s `buildCatalogStore` now branches into
  `buildDashboardCatalogStore` (byte-for-byte the old function) or
  `buildDediCatalogStore`. `catalogsource` itself is untouched — it remains
  the production default.
- **No real DeDi Registry exists yet to crawl against.** `dedi` mode's only
  `Registry` implementation is still `StubRegistry`. To make it demoable end
  to end anyway, `internal/dedicrawl/fixture.go` (new) reads a checked-in
  JSON `Fixture` (`internal/dedicrawl/testdata/sample-fixture.json`),
  self-signs a `CatalogFile`/`CatalogIndex`/`SubscriberRecord` per declared
  subscriber with a keypair generated fresh every process start, and serves
  them over a loopback-only `http.Server` so `Crawler.fetchBytes`'s plain
  HTTP GETs have something real to hit. New config: `DEDI_FIXTURE_PATH`
  (required for `dedi` mode to serve anything — falls back to
  `service.EmbeddedCatalogStore{}` with a logged warning if empty/unreadable,
  never crashes startup), `DEDI_SUBSCRIBER_REFS` (optional subset filter),
  `DEDI_NETWORK_IDS`/`DEDI_SCHEMA_TYPES` (index-scoping), `DEDI_CUTOVER_FRACTION`.
  **This fixture harness is explicitly a local dev/demo tool, not a
  production integration** — same posture as the ephemeral outbound signing
  key in `buildSigner` when `SIGNING_PRIVATE_KEY_BASE64` is unset. All six new
  vars were also added to `docker-compose.yml`'s `environment:` block (it
  only forwards vars explicitly listed there — every existing var already
  had an entry, and the new ones needed the same or they'd be invisible to
  the container despite being in `.env`/`.env.example`).
- **Two spec-fidelity gaps closed in `internal/dedicrawl` itself** (found by
  reading `Catalog_Publishing_and_Discovery.md` directly, not just inferring
  from code):
  1. **Version-regression rejection** (CON-TBD-03/04/11): `applyEntry` had no
     monotonicity check at all. Added `versionRegressed` — best-effort,
     applies only when both the cursor's and the new entry's `entryVersion`
     parse as base-10 integers (this crawler's own tests/fixtures still use
     opaque strings like `"v1"`/`"v2"`, which intentionally skip the check
     rather than false-positive).
  2. **Absence vs. retirement** (CON-TBD-27/35): a catalogId missing from a
     successfully-fetched, validly-signed index (no `retiredAt`) previously
     stayed served forever on stale content. `cursorState` gained an
     `unconfirmed` flag and `markUnseen` (called once per subscriber's full
     crawl, across all its catalog indexes): a dropped-without-tombstone
     catalog stops being served (excluded from `snapshot()`) but its cursor
     state is kept, not erased, so a later crawl that reconfirms it resumes
     serving it. `CrawlSubscriber`'s "no entries indexed" failure check was
     refined alongside this (`crawlIndex` now separately reports `attempted`
     vs. `indexed`) so a *legitimately* empty index no longer gets misreported
     as a crawl failure.
- **Known gap flagged, not fixed here** (out of scope for this pass):
  `cursorEntry.entryVersion` is populated from whichever of
  `entry.Baseline.Version` (fresh/baseline fetch) or `entry.EntryVersion`
  (change-file-chain application) `resolveCatalog` happens to return — these
  are two different version counters per spec (`entryVersion` bumps on *any*
  change; `baseline.version`/change `toVersion` track content lineage
  separately, and the spec's own example has them completely unrelated:
  `entryVersion: 7` alongside `baseline.version: 40`). Whenever they diverge,
  the "unchanged, skip refetch" comparison in `applyEntry` compares
  incompatible values and can spuriously refetch every crawl. Not hit by any
  current fixture/test (which all keep the two aligned) but worth fixing
  before this crawls anything with independently-numbered baseline/entry
  versions.
- **Still explicitly deferred** (per earlier scope agreement, unchanged by
  this pass): a real HTTP-backed `Registry` client, Postgres cursor
  persistence (M2), inbound `/discover` HTTP-signature verification (D15),
  and `MasterRef`/cross-catalog MASTER dependency *resolution* (only
  ordering — MASTER-before-REGULAR — is implemented; a REGULAR catalog's
  declared dependency on an external MASTER catalog's `indexUrl` is never
  actually followed).
- Tests: `internal/dedicrawl` grew from 13 to 18 (regression-rejection,
  absence/reconfirmation, fixture load + end-to-end seeded crawl);
  `internal/config` gained coverage for the five new fields. All 108 tests
  across 13 packages pass, `go vet` clean. Manually verified end to end: a
  local run with `CATALOG_SOURCE_MODE=dedi`+`DEDI_FIXTURE_PATH` set served
  the fixture's one catalog via `GET /discover`; the live production
  container (`CATALOG_SOURCE_MODE` unset) was re-checked immediately after
  and still served its usual 4 dashboard-sourced catalogs, unaffected.

**D19 — In-memory `message.intent` matching, ahead of the Postgres-backed
M3 (2026-09-16).** The user reported `/discover`'s search filter didn't work
as documented. Root cause: `BuildOnDiscover` never looked at `message.intent`
at all — `textSearch`/`filters`/`spatial`/`mediaSearch` were silently
ignored, every configured catalog was always returned. The milestone plan
(§5) scopes real query matching as M3, a full Postgres/PostGIS build-out
(`pg_trgm`/`tsvector` for text, `jsonb_path_query` for filters, PostGIS for
spatial) — but this repo has no database at all yet; catalogs already live
entirely in memory (`CatalogStore.Catalogs()`). So this is a pragmatic,
immediately-useful in-memory fix, not M3 itself — M3's Postgres-backed
version (better performance/indexing at scale) remains the eventual target.

- **New file `internal/service/match.go`**: `matchCatalogs(catalogs,
  intent)` — the core filtering pipeline, applied inside `BuildOnDiscover`
  (both `BuildOnDiscover`/`BuildFixedOnDiscover` gained an `intent
  beckn.Intent` parameter; `ProcessAsync`/`BuildSync` in
  `internal/service/discover.go` now pass `req.Message.Intent` through).
  When `intent` is the zero value, `catalogs` is returned unchanged — every
  existing caller/test that doesn't care about intent is unaffected.
  - **Spatial**: only `op == "S_DWITHIN"` with a `Point` `geometry` and
    `distanceMeters` set is evaluated (Haversine distance against each of
    the catalog's `provider.availableAt[*].geo` points, `quantifier`
    `"ANY"`/`"ALL"`) — the one operator with a concrete worked example
    anywhere in this repo's docs (§6). Any other op/geometry combination is
    treated as **permissively passing**, not a hard failure — an
    unsupported CQL2 operator must never silently zero out every request
    that happens to use it.
  - **textSearch**: case-insensitive, all-whitespace-terms-must-match
    against a resource's `descriptor.name`/`shortDesc`/`longDesc`.
  - **filters**: `{type: "jsonpath", expression}` evaluated via
    `github.com/theory/jsonpath` (new dependency) — an RFC 9535-compliant
    implementation, matching the exact standard `beckn.yaml` cites. The
    expression runs against the catalog's resources as a JSON array (the
    natural shape for a filter-selector like the spec's own
    `$[?(@.rating.value >= 4.0)]` example); resources are matched back by
    `id`.
  - **Offers** are kept if they have no `resourceIds` (catalog-wide) or
    still reference at least one surviving resource; a catalog with zero
    resources and zero offers left after filtering is dropped entirely.
  - **mediaSearch is an explicit no-op** — there is no image/audio
    similarity capability available in-memory. Not silently pretended to
    work; a request that sets it just doesn't get filtered on that
    dimension.
- **Malformed `filters` is now a validation error, not a silent no-op or a
  crash.** New `constants.ErrInvalidIntent` (`SCH_INVALID_INTENT`) and
  `validateIntent` (`internal/service/discover.go`), called from both
  `Validate` and `ValidateSync`: `filters.type` must be `"jsonpath"`,
  `filters.expression` must parse. This has to happen at validation time
  (before the `Ack`), not inside `BuildOnDiscover` — the async `POST
  /discover` path already Acks before any catalog matching runs, so a bad
  expression discovered only in the background could no longer become a
  400. `textSearch`/`spatial` are never hard-rejected (partial support is
  handled permissively at match time, per above).
- **Existing test fixture fixed to not accidentally test the wrong thing**:
  `internal/handlers/discover_test.go`'s shared `validRequestBody` hardcoded
  `textSearch: "laptop"` against the embedded **coffee** demo catalog
  (`internal/service/data/catalog.json`) — harmless while intent was
  ignored, but would have zeroed out results and broken two transport tests
  once real filtering landed. Blanked to `""` so those stay decoupled from
  search relevance; new dedicated tests cover real `textSearch`/`filters`
  behavior against the actual embedded data.
- Explicitly out of scope for this pass: full CQL2 operator/geometry
  coverage (only `S_DWITHIN`/`Point`), `mediaSearch`, returning
  `AckNoCallback` for a zero-match result (a zero-catalog `on_discover` is
  delivered instead — still a valid response), and relevance
  ranking/scoring (matching is boolean, no ordering).
- Tests: `internal/service/match_test.go` (new, 10 cases) + new
  `validateIntent`/`BuildSync` cases in `discover_test.go` + 3 new handler
  tests. 126 tests across 13 packages pass, `-race` clean. Manually verified
  end to end against a local run (embedded demo catalog, not the live
  container): `textSearch=coffee` → both coffee catalogs;
  `textSearch=laptop` → `catalogs: []`, still `200 OK`; a `filters` JSONPath
  expression narrowing to one specific resource ID → exactly that resource,
  in its catalog. The live production container was independently confirmed
  unaffected before and after.

**D20 — Per-client rate limiting + a configurable intent-matching timeout,
closing a DoS gap the D19 review surfaced (2026-09-16).** A senior-review
pass over D19 flagged: before it, every `/discover` request did roughly
constant work (intent was ignored); after it, request cost scales with both
catalog size and the caller-supplied filter's complexity — and since
inbound requests still aren't authenticated (D15), nothing bounded either
how many requests a caller could issue or how long a single expensive
filter could run. Two independent protections, both `.env`-configurable
(CONTEXT.md D7):

- **Rate limiting** — new `internal/handlers/ratelimit.go`, a per-client
  (keyed by remote address, via `golang.org/x/time/rate` token buckets)
  `RateLimiter.Middleware`, applied in `cmd/discovery/main.go` around both
  `/discover` routes (not `/healthz`). Over-limit gets `429` via the
  existing `writeNack` helper with a new `constants.ErrTooManyRequests`
  (`beckn.yaml`'s `NackTooManyRequests` family, listed as a possible
  `/discover` response in §6 but never implemented until now). A background
  goroutine evicts idle clients' limiters after 5 minutes so the tracking
  map can't grow unbounded. New config: `RATE_LIMIT_ENABLED` (default
  `true`), `RATE_LIMIT_REQUESTS_PER_SECOND` (default `5`),
  `RATE_LIMIT_BURST` (default `10`) — comfortably above any single real
  BAP's expected call rate or this demo's own `catalogsource` polling
  traffic.
- **Match timeout** — `DiscoverService` gained a `matchTimeout`
  (`MATCH_TIMEOUT_MILLISECONDS`, default `500ms`). `BuildOnDiscover`/
  `BuildFixedOnDiscover`/`matchCatalogs` (`internal/service/response.go`,
  `match.go`) all gained a leading `context.Context` parameter;
  `matchCatalogs` checks `ctx.Err()` once per catalog in its main loop and
  stops immediately once the deadline is hit, returning whatever's matched
  so far rather than blocking until the full set is processed.
  `BuildSync` wraps the incoming HTTP request's own context
  (`r.Context()`, threaded from `internal/handlers/discover.go`'s
  `ServeSync`) with this timeout; `ProcessAsync` wraps the worker's ctx the
  same way, separately from the existing `deliverCtx`/`deliveryTimeout`
  (which still only bounds the *dispatch* HTTP call afterward, not
  matching). A timeout is logged (`"intent matching timed out, returning
  partial results"`), never silent.
- **Explicitly out of scope**: per-authenticated-identity rate limiting
  (no inbound auth yet, D15 — this is necessarily IP-based, a weaker
  signal); JSONPath expression complexity analysis (the timeout is a
  blunter but sufficient backstop); distributed/shared rate limiting across
  multiple instances (in-memory per-process only, matching every other
  piece of state in this service today).
- Tests: `internal/handlers/ratelimit_test.go` (new, 5 cases: within-burst
  passes, over-burst gets 429/NACK, independent per-client budgets, host:port
  key extraction + its fallback) + one `matchCatalogs` unit test (an
  already-expired context stops matching before anything is processed) + one
  `DiscoverService`-level test proving the wiring end to end (a near-zero
  `matchTimeout` against the real embedded catalog store truncates results,
  not just the isolated unit check). 133 tests across 13 packages pass,
  `-race` clean.
- **D18 bug found and fixed while actually turning `dedi` mode on for the
  first time in Docker**: `DEDI_FIXTURE_PATH` is a relative path, but the
  `Dockerfile`'s final stage only ever copied the compiled binary —
  `internal/dedicrawl/testdata/` never existed inside the container, so
  `dedi` mode would have silently fallen back to the embedded demo catalog
  with only a log warning, never actually crawling anything. Fixed:
  `Dockerfile` now copies `internal/dedicrawl/testdata` into the final image
  at the same relative path (`WORKDIR /app`), and the build stage's base
  image was bumped `golang:1.25-alpine` → `golang:1.26-alpine` (`go.mod`
  had already moved to `go 1.26.0` from this decision's own `golang.org/x/time`
  dependency, which the old build image couldn't satisfy). Verified live:
  the production container now genuinely crawls the fixture and serves its
  one demo catalog when `CATALOG_SOURCE_MODE=dedi` is set in `.env`.

**D21 — A real, live DeDi registry exists and is reachable; `HTTPRegistry`
built against it (2026-09-16).** The user found a `dediregistry` plugin
config from a sibling project (`beckn-onix`) pointing at
`https://fabric.nfh.global/registry/dedi`. Investigated and confirmed real
(`curl` against it returns a proper `404 {"message":"record not found"}`
for a made-up key — not a placeholder), and `beckn-onix`'s own Go client
(`pkg/plugin/implementation/dediregistry/dediregistry.go`) gave the exact
real API shape: `GET {url}/lookup/{namespace}/{registry}/{recordName}` →
`{"data": {"details": {subscriber_id, signing_public_key, url}, "meta":
{catalog_index_urls: [{url}, ...]}}}` — this supersedes the "no real,
reachable DeDi Registry exists" framing in D18/D19/D20.

- **Searched for a real, live subscriber to actually crawl** (checked
  `sample-pn.ayushmatha.in`, seen earlier in an `onix-bap` log during D19's
  investigation): found a genuine local onboarding *toolkit* at
  `Ion Repo/reference-app/dedi-onboarding-files/` (`dedi-file-signer` +
  generated `dedi.json`/`dedi.index.json` matching the real spec shape —
  `dedi_version`, `domain`, `files[]`, `keys[]` JWK, `proof.jws` —
  independently confirming the D18-flagged mismatch between our own
  simplified `DediManifest`/`Signature` types and the real spec shape is
  real). But neither the candidate domain (`ion-ref-app.ayushmatha.in`, DNS
  `NXDOMAIN`) nor its registry record (`fabric.nfh.global` → `404 record
  not found` for
  `ion-ref-app.ayushmatha.in/ion-scratch-registry/sample-pn-ayushmatha-in`)
  actually resolve. **This was local scratch/example tooling output, never
  published live — no confirmed real subscriber to crawl exists yet.**
  Per the user's explicit decision: build the real client anyway (it's
  provably reachable and its shape is now known), don't invent a
  subscriberRef.
- **New `internal/dedicrawl/httpregistry.go`**: `HTTPRegistry` implements
  the existing `Registry` interface unchanged — `crawl.go`/`cursor.go`/
  every existing `StubRegistry`-based test needed zero changes.
  `SubscriberRecord` requires `subscriberRef` in real `namespace/registry/recordName`
  nodeID form (rejects anything else); parses `meta.catalog_index_urls`
  defensively (native `{url}` array, or a JSON-double-encoded string
  containing that same array — a real-world quirk `beckn-onix`'s own
  client also guards against); since the real endpoint has no digest field
  of its own, `HTTPRegistry` self-computes one via the already-exported
  `CanonicalizeJCS`/`Digest` (same self-consistency step the fixture
  already relied on — `verifySubscriberRecord` was already documented as
  "a deliberately simplified trust check ... not a real registry-anchored
  trust chain," unchanged by this). `Manifest` is a structural formality
  (self-signed, same mechanism `StubRegistry` uses, factored into a shared
  `selfSignedManifest` helper) — the real registry has no separate signed
  manifest document; trust here is HTTPS + the registry operator, same as
  any ordinary API.
- **Config**: `DediRegistryMode` (`"fixture"` default / `"http"`),
  `DediRegistryURL` (defaults to the confirmed-reachable public registry).
  `buildDediCatalogStore` (`cmd/discovery/main.go`) branches on it; `http`
  mode with no `DediSubscriberRefs` configured logs a warning and falls
  back to the embedded demo catalog, same "nothing to serve yet" posture
  every other unconfigured path in this function already has.
- **Live `.env` left on `DEDI_REGISTRY_MODE=fixture`** — no behavior
  change to what the running container actually serves; `DEDI_REGISTRY_URL`
  is present and documented, ready to flip once a real subscriberRef is
  confirmed.
- **Still out of scope**: the D18-flagged `CatalogFile`/`IndexEntry` type
  mismatches (`version` int vs. string, `Signature` object vs. string) —
  those apply to a subscriber's *own* catalog files (fetched from whatever
  URL its `catalog_index_urls` point at, not from the registry itself),
  and there's no live catalog index to test a fix against yet regardless.
- Tests: `internal/dedicrawl/httpregistry_test.go` (new, 6 cases: native and
  double-encoded `catalog_index_urls` parsing, malformed-nodeID rejection,
  404 and missing-signing-key errors, `Manifest` self-verification) +
  `DediRegistryMode`/`DediRegistryURL` config coverage. 139 tests across 13
  packages pass, `-race` clean.

## 5. Milestone plan (TDD, step by step)

1. **M1 — Contract skeleton — DONE, then hardened for concurrency
   (2026-09-04).** `POST /discover`: parses + validates
   `context.transactionId`/`messageId`/`bapUri` present, returns `Ack`
   (placeholder signature, see D3) synchronously. A bounded background
   worker then posts a **fixed/mocked** `on_discover` payload (one canned
   catalog/resource/offer, spec-shaped per §6) to `context.bapUri` +
   `/on_discover`. Verified end-to-end via `docker compose up` + a real HTTP
   callback receiver (not just unit tests), including a real SIGTERM
   graceful-shutdown test (see §10).
   - Code layout — see §9 for the full rationale:
     `internal/beckn` (wire types) · `internal/constants` · `internal/utility`
     · `internal/service` (business logic + ports: `OnDiscoverDispatcher`,
     `AsyncRunner`) · `internal/dispatch` (HTTP adapter) · `internal/worker`
     (bounded background-job pool adapter) · `internal/handlers` (HTTP
     transport, incl. `LoggingMiddleware`) · `internal/config` ·
     `internal/logging` (logharbour wrapper, D10) · `cmd/discovery/main.go`
     (wiring). 69 tests across 11 packages (`internal/signing` added in D15),
   all passing under `-race`.
   - Local run: `cp .env.example .env` (once) then `docker compose up --build`
     → service on `http://localhost:8090` (host port 8090 chosen because
     8080 was already in use on the dev machine — override `HOST_PORT` in
     `.env` if that collides elsewhere; see D7). Health check: `GET /healthz`.
   - Known simplification to revisit: `DiscoverService.Validate` only
     checks presence of a few required Context fields via Go zero-values —
     it cannot distinguish "field absent in JSON" from "field present but
     empty string", and does not yet validate `message.intent` shape at all.
     Real JSON-schema validation (against `beckn.yaml`) is deferred, most
     naturally to M2/M3 once there's real query logic to validate intent
     against.
2. **M2 — Real catalog storage.** GORM models + Postgres migrations for
   Catalog/Provider/Resource/Offer (JSONB `resourceAttributes`/`descriptor`).
   A minimal publish/seed path to populate them.
3. **M3 — Real query matching.** `textSearch` (pg_trgm/tsvector) →
   `filters` (JSONPath via `jsonb_path_query`) → `spatial` (PostGIS) —
   built and tested independently, then combined.
4. **M4 — Real Ed25519 auth.** Signature verification on inbound `/discover`,
   real CounterSignature on `Ack`, real signing on outbound `/on_discover`.
5. **M5 — Ingestion strategy decision.** Simple publish endpoint vs.
   decentralized DeDi crawler reuse — revisit with real data in hand.

## 6. The `/discover` ↔ `/on_discover` contract (source: ion-specs `beckn.yaml`)

### Request: `POST {ourBaseUrl}/discover`

```json
{
  "context": {
    "domain": "retail",
    "action": "discover",
    "version": "2.0.0",
    "bapId": "bap.example.com",
    "bapUri": "https://bap.example.com",
    "bppId": "bpp.example.com",
    "bppUri": "https://bpp.example.com",
    "transactionId": "3f7a1c1e-....-uuid",
    "messageId": "9b2e....-uuid",
    "networkId": "ion.id/ion-network",
    "timestamp": "2026-09-04T10:00:00Z",
    "ttl": "PT30S",
    "schemaContext": ["https://schema.ion.id/context/retail.jsonld"]
  },
  "message": {
    "intent": {
      "textSearch": "gaming laptop premium tech",
      "filters": {
        "type": "jsonpath",
        "expression": "$[?(@.rating.value >= 4.0)]"
      },
      "spatial": [
        {
          "op": "S_DWITHIN",
          "targets": "$['availableAt'][*]['geo']",
          "geometry": { "type": "Point", "coordinates": [77.5946, 12.9716] },
          "distanceMeters": 1500,
          "quantifier": "ANY"
        }
      ],
      "mediaSearch": { "media": [], "options": {} }
    }
  }
}
```
`intent` is required; `textSearch`/`filters`/`spatial`/`mediaSearch` are all
optional and independently combinable. Also accepts a legacy flat
`message.textSearch` (deprecated alias, not `message.intent.textSearch`).

### Synchronous response to the request: `200 Ack`

```json
{
  "status": "ACK",
  "signature": "Signature keyId=\"bpp.example.com|key-1|ed25519\",algorithm=\"ed25519\",created=\"1735900000\",expires=\"1735900300\",headers=\"(created) (expires) digest\",signature=\"base64...\""
}
```
`status` is `ACK` or `NACK`; `signature` (`CounterSignature`) is **required**
even on ACK — it proves we authenticated and processed the request.
**`signature` is a STRING** (per `beckn.yaml`'s `Signature` schema:
`type: string`, a specific draft-cavage-http-signatures-12-formatted
pattern — see D12), not a nested JSON object; an earlier version of this
doc (and of the code) got this wrong. NACK responses additionally carry
an `error: { errorCode, errorMessage }`. Until D3 (auth) lands, we emit a
structurally valid but explicitly-fake signature (`beckn.FormatSignature`
+ `service.PlaceholderSignature`).

Failure modes per the spec: `400 NackBadRequest`, `401 NackUnauthorized`,
`409 AckNoCallback` (accepted, but no callback will follow — e.g. nothing to
search), `500 ServerError`.

### Callback we send: `POST {context.bapUri}/on_discover`

```json
{
  "context": { "...same shape, action": "on_discover", "...": "..." },
  "message": {
    "catalogs": [
      {
        "id": "catalog-electronics-001",
        "bppId": "bpp.example.com",
        "bppUri": "https://bpp.example.com",
        "isActive": true,
        "descriptor": { "name": "Electronics Catalog" },
        "provider": {
          "id": "tech-store-001",
          "descriptor": { "name": "Tech Store" },
          "availableAt": [
            { "geo": { "type": "Point", "coordinates": [77.5946, 12.9716] } }
          ]
        },
        "resources": [
          {
            "id": "res-1",
            "descriptor": { "name": "Premium Gaming Laptop Pro" },
            "resourceAttributes": {
              "@context": "https://schema.ion.id/context/retail.jsonld",
              "@type": "Product",
              "brand": "Premium Tech"
            }
          }
        ],
        "offers": [
          {
            "id": "offer-1",
            "resourceIds": ["res-1"],
            "descriptor": { "name": "10% off" }
          }
        ]
      }
    ]
  }
}
```
`Catalog` requires `id`, `descriptor`, `provider`, and at least one of
`resources`/`offers`. `Resource.resourceAttributes` and `Provider.providerAttributes`
are open JSON-LD bags (`@context`+`@type` required, everything else
domain-specific) — this is intentionally where per-sector fields (EV
charging, logistics, etc.) live without changing the core schema.

The response to *our own* callback POST is another `Ack` (same shape as
above), sent back to us by the BAP.

## 7. Architectural lessons borrowed from beckn-discovr (not its stack)

- **Registry-resolved callback URL, never trust `context.bapUri` blindly**
  once auth exists — SSRF risk. For now (auth stubbed) we do use `bapUri`
  directly, but flag this as a known gap to close in M4.
- **Catalog replace semantics**: support `FULL` (delete+reinsert scoped to
  `catalogId`) and `MERGE` (RFC 7396 JSON Merge Patch) from M2 onward, scoped
  strictly per `catalogId` — never per `bppId` (a BPP can own multiple
  catalogs).
- **Idempotency on `messageId`**: dedupe retried `/discover` POSTs within a
  short TTL window so retries don't trigger duplicate processing/callbacks.
- **Structured logging** with a consistent field set
  (`transactionId`, `messageId`, `catalogId`, `networkId`, `subscriberId`) —
  adopt early even before auth exists, since transactionId/messageId are
  already on every request.
- **Constructor injection only** — no framework magic DI; matches D5 and the
  org's existing POC style.

## 8. Open questions to revisit (not blocking M1)

- Exact ingestion path long-term: simple publish API vs. reusing/extending
  the decentralized DeDi crawler POC vs. both.
- Whether ION's network profile (`ion.yaml` `x-ion-profile`) imposes anything
  stricter than core Beckn on `/discover` itself (so far: no — ION's L3
  extensions are `/raise` and `/reconcile` only; `/discover` is untouched
  core Beckn v2).
- Text search engine choice once Postgres full-text isn't enough (own
  decision later, not Elasticsearch by default — no committed direction yet).

## 9. Directory layout

This is a **layered Go project layout**: `cmd/` for entry points and
`internal/` split by technical layer (config / constants / utility /
domain model / business logic / infrastructure adapters / HTTP transport),
combined with **Ports and Adapters (Hexagonal) architecture** for how those
layers depend on each other (D8). This combination — layer-per-directory
plus interface-owned-by-the-inner-layer — is what `golang-standards/project-layout`
and most current (2026) Go service write-ups describe as the default
"Clean/Hexagonal Architecture" shape for a Go web service, typically with
`/api` or `/handler` (HTTP), `/service` (business logic), `/repository` or
`/store` (data access), and `/domain` (core types) under `internal/`. We
follow that shape with names chosen to be unambiguous to read, restated
per explicit user instruction:

```
cmd/discovery/main.go     entry point — wiring ONLY (build adapters, inject
                          into service, start/stop the HTTP server). No
                          business logic here, ever.

internal/
  config/                 loads .env-backed configuration (internal/config/config.go)
  constants/              global constant values — action names, error
                          codes, header/content-type strings (see note below
                          on the name)
  utility/                small, generic, dependency-free helpers (JSON
                          response writing today) — deliberately kept
                          narrow; a grab-bag "misc utils" package is an
                          anti-pattern to avoid growing into
  beckn/                  the Beckn v2 wire/domain types (Context, Intent,
                          Catalog, Ack, ...) — shared vocabulary every other
                          layer imports; not one of the boxes above because
                          it's protocol schema, not application logic
  service/                business logic + the PORT interfaces it depends
                          on (OnDiscoverDispatcher, AsyncRunner). Owns
                          decisions, never does raw I/O itself.
  dispatch/               ADAPTER: delivers on_discover over HTTP
                          (implements service.OnDiscoverDispatcher)
  worker/                 ADAPTER: bounded background-job pool
                          (implements service.AsyncRunner) — a generic
                          concurrency primitive, reused by any future
                          background work, so it's its own package rather
                          than folded into service/ or dispatch/
  handlers/               HTTP transport: decode/encode JSON, map
                          service-layer results to status codes. No
                          business logic.
```

Two naming notes, so a reader isn't confused later:
- **`constants/`, not `const/`.** `const` is a reserved Go keyword — a
  package literally cannot be named `const` (`package const` fails to
  compile). The directory holds exactly what was asked for (global
  constants/config-adjacent literals); it's just spelled `constants`.
- **`utility/` is intentionally small.** Community Go guidance in 2026
  generally warns against a generic `utils`/`helpers` package growing
  into an unstructured dumping ground with no cohesive purpose. We keep
  the name (as asked) but the *rule* is: a new helper gets its own
  clearly-named file in this package, and if it stops being generic
  (e.g. becomes Beckn-specific), it moves to a more specific package
  instead of enlarging this one.

One more directory will very likely appear in M2 without changing this
scheme: `internal/store/` (or `internal/repository/`) holding a
`CatalogStore` port in `service/` plus a `postgres/` (and, hypothetically,
`clickhouse/`) adapter package — exactly the ClickHouse/Postgres swap
example from D8, just not needed until there's a real store to swap.

Sources consulted for current (2026) Go project-layout conventions:
[How to Structure Go Projects for Maintainability](https://oneuptime.com/blog/post/2026-01-07-go-project-structure/view),
[Go Project Structure 2026: Clean Architecture & Best Practices](https://reintech.io/blog/go-project-structure-2026-clean-architecture-best-practices),
[Go Project Structure: Practices & Patterns](https://www.glukhov.org/app-architecture/code-architecture/go-project-structure/),
[golang-standards/project-layout](https://github.com/golang-standards/project-layout).
One point those sources make that we are *knowingly diverging from*: 2026
guidance leans toward organizing `internal/` **by domain/feature**
(`internal/user/`, `internal/order/`) rather than by technical layer, to
avoid a "shotgun surgery" feel as the codebase grows. We're staying
layer-based per explicit user instruction, since this service currently
has exactly one real domain (discover) — revisit this if/when a second
distinct domain (e.g. catalog ingestion in a later milestone) makes
layer-based folders start mixing unrelated concerns in the same directory.

## 10. Concurrency review (D9) — defects found and fixed

Requested explicitly: use goroutines for what they're good at (the
on_discover callback must not block the /discover response), but audit
for the standard ways Go concurrency goes wrong in production, and fix
each one rather than leaving it as a TODO.

1. **Unbounded goroutine fan-out.** The first M1 cut spawned a bare
   `go func(){...}()` per `/discover` request with no ceiling. A traffic
   burst would open an unbounded number of outbound HTTP connections and
   goroutines simultaneously — a resource-exhaustion vector, and also
   makes load impossible to reason about.
   **Fix**: `internal/worker.Pool` — a fixed number of worker goroutines
   (`WORKER_POOL_SIZE`, default 10) consuming from a bounded queue
   (`WORKER_QUEUE_SIZE`, default 100). `Run()` never blocks the caller: it
   returns `false` immediately if the queue is full, and the caller
   (`DiscoverService.ProcessAsync`) logs a warning and drops that
   delivery rather than hanging the request path or growing unbounded.
2. **Unrecovered panic crashes the whole process.** Go does not isolate
   a goroutine's panic — if a bare `go func(){...}()` panics and nothing
   recovers inside that goroutine, the entire process crashes, taking
   down every other in-flight request with it. The original fixed-response
   builder is simple enough to be unlikely to panic today, but nothing
   guaranteed that would stay true as real query logic lands in M3.
   **Fix**: `worker.Pool.runJob` wraps every job in `defer func(){ recover() }()`
   and logs the panic instead of propagating it. Verified by
   `TestPool_Run_RecoversPanicAndKeepsProcessingSubsequentJobs`, which
   panics on purpose and asserts the pool is still alive afterward.
3. **No graceful shutdown.** `main.go` originally called
   `http.ListenAndServe` directly with no signal handling — a
   `docker compose down` / k8s pod eviction (SIGTERM) would kill the
   process mid-flight, silently dropping any on_discover callback that
   was still being delivered.
   **Fix**: `main.go` now runs the server in a goroutine, traps
   `SIGINT`/`SIGTERM` via `signal.Notify`, then calls `srv.Shutdown(ctx)`
   (stop accepting new connections, finish in-flight ones) followed by
   `pool.Shutdown(ctx)` (drain queued/running dispatch jobs), both bounded
   by `SHUTDOWN_TIMEOUT_SECONDS` (default 15s; `docker-compose.yml` sets
   `stop_grace_period: 20s` so Docker's own SIGKILL fallback doesn't race
   ahead of our graceful window). **Verified for real**, not just by
   inspection: ran the containerized service, sent it a live `/discover`
   request, then `docker compose stop` and confirmed the log sequence
   `shutdown signal received` → `discovery-service stopped` with no
   forced kill.
4. **A subtle bug caught *during* fixing defect 3.** The first draft of
   `Pool.Shutdown` cancelled the shared worker context on deadline, then
   still did a second blocking `<-done` waiting for `wg.Wait()` to
   return — but a job that never checks its `ctx` argument (or blocks on
   something that doesn't) will never return, so `wg.Wait()` never
   returns either, and `Shutdown` hangs *forever* regardless of the
   deadline passed in. Caught by an actual test
   (`TestPool_Shutdown_ReturnsContextErrorWhenJobsExceedDeadline`) timing
   out rather than by inspection — a good reminder that concurrency bugs
   like this are easy to write and easy to miss without a test that
   actually exercises the timeout path.
   **Fix**: `Shutdown` returns `ctx.Err()` as soon as the deadline fires,
   without waiting further. This is a deliberate, documented trade-off:
   **Go cannot forcibly kill a goroutine** — cancellation is only ever
   cooperative — so a bounded shutdown deadline must actually be bounded,
   even if that means a misbehaving job's goroutine leaks until it
   eventually unblocks on its own (or the process exits entirely). In
   practice every job we schedule is built on context-aware I/O
   (`http.NewRequestWithContext`), so real jobs do abort promptly; this
   path is a safety net for a future job that doesn't, not the common case.
5. **`http.Server` had no timeouts.** An `http.Server` with
   `ReadTimeout`/`WriteTimeout`/`ReadHeaderTimeout`/`IdleTimeout` all
   unset will hold a slow or malicious client's connection (and its
   goroutine) open indefinitely — a well-known Go footgun sometimes
   called "the http.Server your parents warned you about."
   **Fix**: all four are set explicitly in `main.go`, sourced from
   config (`HTTP_READ_HEADER_TIMEOUT_SECONDS` etc., see `.env.example`).
6. **Unbounded request body read.** `json.NewDecoder(r.Body).Decode(...)`
   with no size limit reads an attacker- or bug-supplied body fully into
   memory before validation ever gets a chance to reject it — a single
   oversized request could exhaust process memory.
   **Fix**: `handlers.DiscoverHandler` wraps the body in
   `http.MaxBytesReader` bounded by `MAX_REQUEST_BODY_BYTES` (default
   1 MiB), verified by `TestDiscoverHandler_OversizedBody_ReturnsNack400`.

Not changed, and why: `DiscoverService.ProcessAsync` builds its
`context.WithTimeout` from `context.Background()` (via the pool's shared
worker context), not from the original `http.Request`'s context. This is
intentional, not an oversight — by the time the background job runs, the
HTTP response has already been written and the request's context may
already be cancelled (client disconnect, response flushed); tying
delivery to it would make every delivery race its own cancellation for no
benefit. The pool's own context (cancelled only on `Shutdown`) is the
correct lifetime to inherit from.
