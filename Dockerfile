# The image of flovr that releases publish as ghcr.io/flovrlabs/flovr.
# It serves the workflows in /work, where a folder of them is mounted:
#
#   docker run -p 8080:8080 -v "$PWD:/work" -e FLOVR_TOKEN -e OPENROUTER_API_KEY ghcr.io/flovrlabs/flovr
#
# The examples are in /examples, so they can be tried without a key:
#
#   docker run --rm ghcr.io/flovrlabs/flovr test /examples/triage-ticket --replay

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY schema schema
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /flovr ./cmd/flovr

FROM alpine:3.24
# git takes components from git, and tells which commit of the files each run
# ran. The folder mounted at /work belongs to another user, which git would
# otherwise refuse to read.
RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory /work \
    && adduser -D -u 10001 flovr
COPY --from=build /flovr /usr/local/bin/flovr
COPY examples /examples
COPY LICENSE /usr/share/doc/flovr/LICENSE
USER flovr
WORKDIR /work
EXPOSE 8080
ENTRYPOINT ["flovr"]
# Beyond the container the server needs FLOVR_TOKEN, which webhook
# requests must carry.
CMD ["serve", "--addr", "0.0.0.0:8080", "."]
