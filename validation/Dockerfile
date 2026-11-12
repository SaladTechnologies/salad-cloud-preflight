# syntax=docker/dockerfile:1
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/salad-preflight ./cmd/salad-preflight

# glibc supports the NVIDIA utilities injected by the GPU runtime.
FROM python:3.13-slim-bookworm
ENV NVIDIA_VISIBLE_DEVICES=all NVIDIA_DRIVER_CAPABILITIES=compute,utility PYTHONUNBUFFERED=1
COPY --from=build /out/salad-preflight /usr/local/bin/salad-preflight
COPY validation/server.py validation/benchmark.py validation/gpu-load.py validation/vram-hold.py /opt/validation/
RUN chmod 755 /opt/validation/benchmark.py
EXPOSE 8080
ENTRYPOINT ["python3", "/opt/validation/server.py"]
