FROM golang:1.26.10-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod -o /out/main .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/main /app/main
COPY src /app/src
COPY config.example.json /app/config.example.json
COPY docker-start.sh /app/docker-start.sh

RUN chmod +x /app/main /app/docker-start.sh

EXPOSE 8080

CMD ["/app/docker-start.sh"]
