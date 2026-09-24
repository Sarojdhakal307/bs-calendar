# syntax=docker/dockerfile:1.7
# Build context: repository root (the service embeds /api and /fixtures).
#   docker build -f services/calendar-api/Dockerfile -t bs-calendar/calendar-api .

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api ./api
COPY fixtures ./fixtures
COPY services ./services
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/calendar-api ./services/calendar-api/cmd/calendar-api

# Minimal runtime: no shell, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/calendar-api /calendar-api
USER nonroot:nonroot
EXPOSE 8080 9090
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=3 CMD ["/calendar-api", "healthcheck"]
ENTRYPOINT ["/calendar-api"]
CMD ["serve"]
