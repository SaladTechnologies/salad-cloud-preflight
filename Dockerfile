# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/salad-preflight ./cmd/salad-preflight

# Artifact-only image. Copy the binary into the customer's application image.
FROM scratch AS binary
COPY --from=build /out/salad-preflight /salad-preflight
