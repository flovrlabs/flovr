# The image of flowmaker that releases publish as ghcr.io/simonyosb/flowmaker.
# It serves the workflows in /work, where a folder of them is mounted:
#
#   docker run -p 8080:8080 -v "$PWD:/work" -e FLOWMAKER_TOKEN -e OPENROUTER_API_KEY ghcr.io/simonyosb/flowmaker
#
# The examples are in /examples, so they can be tried without a key:
#
#   docker run --rm ghcr.io/simonyosb/flowmaker test /examples/triage-ticket --replay

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /flowmaker ./cmd/flowmaker

FROM alpine:3.24
# git takes components from git, and tells which commit of the files each run
# ran. The folder mounted at /work belongs to another user, which git would
# otherwise refuse to read.
RUN apk add --no-cache ca-certificates git \
    && git config --system --add safe.directory /work \
    && adduser -D -u 10001 flowmaker
COPY --from=build /flowmaker /usr/local/bin/flowmaker
COPY examples /examples
COPY LICENSE /usr/share/doc/flowmaker/LICENSE
USER flowmaker
WORKDIR /work
EXPOSE 8080
ENTRYPOINT ["flowmaker"]
# Beyond the container the server needs FLOWMAKER_TOKEN, which webhook
# requests must carry.
CMD ["serve", "--addr", "0.0.0.0:8080", "."]
