FROM golang:1.26.6-alpine AS builder

WORKDIR /app

RUN apk add --no-cache ca-certificates git

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/clawguard ./cmd/clawguard
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/migrate ./cmd/migrate

FROM alpine:3.20

# wget and grep are BusyBox applets already present in alpine; the compose
# healthcheck relies on them. Keep them working when trimming this image.
RUN apk add --no-cache ca-certificates tzdata font-dejavu ffmpeg \
    && addgroup -S -g 10001 clawguard \
    && adduser -S -D -H -u 10001 -G clawguard clawguard \
    && command -v wget >/dev/null \
    && command -v grep >/dev/null

WORKDIR /app

COPY --from=builder /out/clawguard /usr/local/bin/clawguard
COPY --from=builder /out/migrate /usr/local/bin/migrate
COPY --from=builder /app/migrations /app/migrations
COPY --from=builder /app/entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 8080

USER 10001:10001

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
