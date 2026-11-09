# syntax=docker/dockerfile:1
#
# One image with a plugin-capable Bifrost gateway, the session guard plugin and the demo.
#
#   docker build -t bifrost-session-guard .
#   docker run --rm -it -p 8080:8080 -p 9797:9797 bifrost-session-guard          # the demo
#   docker run --rm bifrost-session-guard e2e                                     # end-to-end tests
#   docker run --rm -p 8080:8080 -v "$PWD/data:/app/data" bifrost-session-guard gateway
#
# The gateway is built from Bifrost's source with dynamic linking, which is what Bifrost
# requires for custom plugins. The plugin is built in the same stage with the same toolchain
# and checked against the gateway binary.

ARG GO_VERSION=1.27.3

FROM node:22-bookworm AS build
ARG GO_VERSION
ARG TARGETARCH
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local GOWORK=off
WORKDIR /src

# The gateway only changes with the Bifrost version, so it gets its own cached layer.
COPY BIFROST_VERSION ./
COPY scripts/build-gateway.sh scripts/
RUN scripts/build-gateway.sh

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./... && scripts/build-plugin.sh

FROM node:22-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /src/.work/bin/bifrost-http /app/.work/bin/bifrost-http
COPY --from=build /src/build/session-guard.so /app/build/session-guard.so
COPY demo /app/demo
COPY test /app/test
COPY docker/entrypoint.sh /app/entrypoint.sh
# Inside a container the listeners must bind all interfaces to be reachable through -p.
ENV GW_HOST=0.0.0.0 STATUS_ADDR=0.0.0.0:9797
EXPOSE 8080 9797
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["demo"]
