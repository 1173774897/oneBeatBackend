# syntax=docker/dockerfile:1

ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-alpine AS build

ARG APP_ENV=prod
ARG APP_VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/onebeat-api \
    ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/onebeat-reconciler \
    ./cmd/reconciler

FROM alpine:3.21

ARG APP_ENV=prod
ARG APP_VERSION=dev

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 onebeat \
    && adduser -S -D -H -u 10001 -G onebeat onebeat

WORKDIR /app
COPY --from=build /out/onebeat-api /app/onebeat-api
COPY --from=build /out/onebeat-reconciler /app/onebeat-reconciler

ENV APP_ENV=${APP_ENV} \
    APP_VERSION=${APP_VERSION} \
    STORE_API_ADDR=:8443 \
    STORE_API_TLS_CERT=/certs/fullchain.pem \
    STORE_API_TLS_KEY=/certs/privkey.pem

USER 10001:10001

EXPOSE 8443

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
  CMD wget --no-check-certificate --quiet --spider https://127.0.0.1:8443/healthz || exit 1

ENTRYPOINT ["/app/onebeat-api"]
