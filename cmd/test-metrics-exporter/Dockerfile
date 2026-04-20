# Synthetic Prometheus exporter for NTNSlice E2E tests.
# Build locally with: docker build -t test-metrics-exporter:latest -f cmd/test-metrics-exporter/Dockerfile .
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/test-metrics-exporter ./cmd/test-metrics-exporter

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/test-metrics-exporter /test-metrics-exporter
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/test-metrics-exporter"]
