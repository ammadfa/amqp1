# RoadRunner with Custom AMQP1 Plugin Dockerfile
# https://docs.docker.com/buildx/working-with-buildx/
# TARGETPLATFORM if not empty OR linux/amd64 by default
FROM --platform=${TARGETPLATFORM:-linux/amd64} golang:1.25-alpine as builder

# app version and build date must be passed during image building (version without any prefix).
# e.g.: `docker build --build-arg "APP_VERSION=1.2.3" --build-arg "BUILD_TIME=$(date +%FT%T%z)" .`
ARG APP_VERSION="undefined"
ARG BUILD_TIME="undefined"

# minimal build deps
RUN apk add --no-cache git ca-certificates

# copy your configuration into the docker
COPY velox.toml /src/velox.toml

WORKDIR /src

# Clone velox and build vx CLI from master, then use it to build rr per velox.toml
RUN git clone --depth 1 https://github.com/roadrunner-server/velox.git /src/velox

# arguments to pass on each go tool link invocation
ENV LDFLAGS="-s -X github.com/roadrunner-server/velox/v2025/internal/version.version=$APP_VERSION -X github.com/roadrunner-server/velox/v2025/internal/version.buildTime=$BUILD_TIME"

WORKDIR /src/velox
RUN set -x \
    && go env -w GOPROXY=https://proxy.golang.org,direct \
    && go mod download \
    && CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o /usr/local/bin/vx ./cmd/vx

# ensure vx is available and use it to build rr according to velox.toml
ENV CGO_ENABLED=0
ARG GITHUB_TOKEN
ENV GITHUB_TOKEN=${GITHUB_TOKEN}
RUN /usr/local/bin/vx build -c /src/velox.toml -o /usr/local/bin/

FROM --platform=${TARGETPLATFORM:-linux/amd64} php:8.3-cli

# Install minimal runtime deps
RUN apt-get update && apt-get install -y ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# copy required files from builder image
COPY --from=builder /usr/local/bin/rr /usr/bin/rr

# Create working directory
WORKDIR /app

# use roadrunner binary as image entrypoint
ENTRYPOINT ["/usr/bin/rr"]
CMD ["serve"]
