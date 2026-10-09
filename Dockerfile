FROM golang:1.26.3-alpine3.22 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/cinlan-qq-bot \
    ./cmd/cinlan-qq-bot

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser -S -G app app

WORKDIR /app
COPY --from=build /out/cinlan-qq-bot /usr/local/bin/cinlan-qq-bot
RUN mkdir -p /app/data \
    && chown -R app:app /app

ENV QQ_PLATFORM=onebot \
    HTTP_LISTEN_ADDR=:8080

USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/cinlan-qq-bot"]
