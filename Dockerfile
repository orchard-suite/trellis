# One static binary: the CLI and the board, with the page and its assets inside it.
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
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$version" -o /out/trellis ./cmd/trellis \
 && mkdir -p /out/tuf

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/trellis /app/trellis
# The sigstore trusted root is cached here; the deployment mounts a
# scratch volume over it so the read-only root filesystem stays read-only.
COPY --from=build --chown=65532:65532 /out/tuf /tuf
ENV TRELLIS_CONFIG=/etc/trellis/config.yaml TUF_ROOT=/tuf HOME=/tuf
EXPOSE 8080 8081
USER nonroot
ENTRYPOINT ["/app/trellis"]
CMD ["serve"]
