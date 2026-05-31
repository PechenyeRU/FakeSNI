# syntax=docker/dockerfile:1

# Build natively on the runner and cross-compile to the target arch (no QEMU).
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev COMMIT=none BUILDTIME=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath \
      -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILDTIME}" \
      -o /fakesni .

# Static binary, no runtime deps (no TLS dial, no CA certs needed).
FROM scratch
COPY --from=build /fakesni /fakesni
ENTRYPOINT ["/fakesni"]
CMD ["-config", "/config.json"]
