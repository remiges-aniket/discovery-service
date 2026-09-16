FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/discovery ./cmd/discovery

FROM alpine:3.20
WORKDIR /app
# internal/dedicrawl/testdata is the checked-in dev/demo fixture DEDI_FIXTURE_PATH
# points at (see CONTEXT.md D18/D20) — a relative path, so it must exist at the
# same relative location under the process's own working directory at runtime.
COPY --from=build /src/internal/dedicrawl/testdata ./internal/dedicrawl/testdata
COPY --from=build /out/discovery /usr/local/bin/discovery
EXPOSE 8080
ENTRYPOINT ["discovery"]
