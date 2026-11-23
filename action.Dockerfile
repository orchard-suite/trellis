# The image `trellis check` runs in from another repository's pipeline: the
# same binary as the board, beside git, which check reads the checkout and
# the latest release tag with.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION trellis.go blueprints.yaml ./
COPY packs ./packs
COPY cmd ./cmd
COPY internal ./internal
RUN version=$(cut -d' ' -f1 VERSION) \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$version" -o /out/trellis ./cmd/trellis

FROM alpine:3.22
RUN apk add --no-cache git && git config --system --add safe.directory '*'
COPY --from=build /out/trellis /usr/local/bin/trellis
ENTRYPOINT ["/usr/local/bin/trellis"]
