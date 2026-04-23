FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod -o /out/main .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/main /app/main
COPY src /app/src
COPY config.example.json /app/config.example.json
COPY railway-start.sh /app/railway-start.sh

RUN chmod +x /app/main /app/railway-start.sh

EXPOSE 8080

CMD ["/app/railway-start.sh"]
