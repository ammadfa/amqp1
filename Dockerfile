# RoadRunner with Custom AMQP1 Plugin Dockerfile
# https://docs.docker.com/buildx/working-with-buildx/
# TARGETPLATFORM if not empty OR linux/amd64 by default
FROM --platform=${TARGETPLATFORM:-linux/amd64} ghcr.io/roadrunner-server/velox:latest as velox

# app version and build date must be passed during image building (version without any prefix).
# e.g.: `docker build --build-arg "APP_VERSION=1.2.3" --build-arg "BUILD_TIME=$(date +%FT%T%z)" .`
ARG APP_VERSION="undefined"
ARG BUILD_TIME="undefined"

# copy your configuration into the docker
COPY velox.toml .

# we don't need CGO
ENV CGO_ENABLED=0

# Allow Go toolchain upgrade to handle version requirements
ENV GOTOOLCHAIN=auto

# Set GitHub token for accessing the repository (if needed)
# You can pass this as a build arg or set it in your environment
ARG RT_TOKEN
ENV RT_TOKEN=${RT_TOKEN}

# RUN build
RUN vx build -c velox.toml -o /usr/local/bin/

FROM --platform=${TARGETPLATFORM:-linux/amd64} php:8.3-cli

# Install any additional dependencies if needed
RUN apt-get update && apt-get install -y \
    && rm -rf /var/lib/apt/lists/*

# copy required files from builder image
COPY --from=velox /usr/local/bin/rr /usr/bin/rr

# Create working directory
WORKDIR /app

# use roadrunner binary as image entrypoint
ENTRYPOINT ["/usr/bin/rr"]
CMD ["serve"]
