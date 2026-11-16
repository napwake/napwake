# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.26.11@sha256:957952ae74dc010608717144399a59a2c6e82ea03d63be1c7ff6577c5a135f93 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Declared here, not with the platform arguments, so a new version doesn't
# invalidate the downloaded modules' layer.
ARG VERSION=dev
# Cross-compiled on the build host: emulating the target platform under QEMU
# makes an arm64 build many times slower and gains nothing for a CGO-free binary.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/LICENSE /workspace/NOTICE /licenses/
USER 65532:65532

ENTRYPOINT ["/manager"]
