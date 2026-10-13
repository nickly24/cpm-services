# Isolated S3 integration-test fixture, not the product's object storage.
# Official MinIO RELEASE.2025-10-15T17-29-55Z, pinned to its exact commit.
# https://github.com/minio/minio/releases/tag/RELEASE.2025-10-15T17-29-55Z
FROM golang:1.27.1-bookworm AS build
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for build_attempt in 1 2 3; do \
      CGO_ENABLED=0 GOMAXPROCS=2 go install -p=2 -trimpath -ldflags="-s -w" \
        github.com/minio/minio@9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a && break; \
      test "$build_attempt" -lt 3 || exit 1; \
    done

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir /data && chown 65532:65532 /data
COPY --from=build /go/bin/minio /minio
USER 65532:65532
EXPOSE 9000 9001
ENTRYPOINT ["/minio"]
