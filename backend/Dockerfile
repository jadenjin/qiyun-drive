FROM golang:1.26-bookworm AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pan-api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pan-worker ./cmd/worker

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl libvips-tools libimage-exiftool-perl \
 && useradd --create-home --uid 10001 pan \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/pan-api /usr/local/bin/pan-api
COPY --from=build /out/pan-worker /usr/local/bin/pan-worker
EXPOSE 8080
USER pan
CMD ["pan-api"]
