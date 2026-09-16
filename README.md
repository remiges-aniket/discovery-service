# discovery-service

A Go implementation of a Beckn **Discovery Service (DS)**: it serves
`POST /discover` requests and answers them asynchronously via
`POST /on_discover` to the caller's callback URL, per Beckn Protocol v2.0.0.

See [CONTEXT.md](CONTEXT.md) for the full architecture, decision log, and
protocol details.

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
