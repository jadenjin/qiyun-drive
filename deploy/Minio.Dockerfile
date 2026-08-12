FROM golang:1.24.8-bookworm AS build

ARG MINIO_VERSION=RELEASE.2025-10-15T17-29-55Z
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

RUN apt-get update \
 && apt-get install -y --no-install-recommends git \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth 1 --branch "${MINIO_VERSION}" https://github.com/minio/minio.git .
RUN LDFLAGS="$(go run buildscripts/gen-ldflags.go)" \
 && CGO_ENABLED=0 go build -tags kqueue -trimpath --ldflags "${LDFLAGS}" -o /out/minio .

# Reuse MinIO's established entrypoint and runtime filesystem, but replace the
# legacy binary with the security-fixed source build above.
FROM minio/minio:RELEASE.2025-07-23T15-54-02Z
ARG MINIO_VERSION=RELEASE.2025-10-15T17-29-55Z
LABEL org.opencontainers.image.source="https://github.com/minio/minio" \
      org.opencontainers.image.version="${MINIO_VERSION}"
COPY --from=build /out/minio /usr/bin/minio
