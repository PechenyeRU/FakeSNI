FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o fakesni .

# Runtime
FROM alpine:3.23.4

RUN apk add --no-cache \
    iptables \
    iproute2 \
    ca-certificates

WORKDIR /app

COPY --from=builder /app/fakesni .

COPY config.json .

EXPOSE 40443

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD netstat -ln | grep :40443 || exit 1

ENTRYPOINT ["./fakesni"]