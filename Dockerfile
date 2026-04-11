# Dockerfile — Platform operator (distroless).
#
# Platform is a long-running Deployment in seam-system. It manages TalosCluster
# and tenant namespace lifecycle, and drives the Seam Infrastructure Provider for
# CAPI-managed target clusters. Distroless: zero attack surface. INV-022.
# platform-schema.md §3.

FROM golang:1.25 AS builder
WORKDIR /build
COPY platform/ .
COPY conductor/ ../conductor/
COPY seam-core/ ../seam-core/
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /bin/ont-platform \
    ./cmd/ont-platform

FROM gcr.io/distroless/base:nonroot
COPY --from=builder /bin/ont-platform /usr/local/bin/ont-platform

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/ont-platform"]
