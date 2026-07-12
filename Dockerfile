# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/birdweather-exporter .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/birdweather-exporter /birdweather-exporter
EXPOSE 9743
USER nonroot
ENTRYPOINT ["/birdweather-exporter"]
