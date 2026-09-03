FROM golang:1.26.6-alpine AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
ENV GOMAXPROCS=1 GOFLAGS=-p=1
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pan-api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pan-worker ./cmd/worker

FROM alpine:3.22
RUN apk upgrade --no-cache \
 && apk add --no-cache ca-certificates curl exiftool vips-tools \
 && adduser -D -u 10001 pan
COPY --from=build /out/pan-api /usr/local/bin/pan-api
COPY --from=build /out/pan-worker /usr/local/bin/pan-worker
EXPOSE 8080
USER pan
CMD ["pan-api"]
