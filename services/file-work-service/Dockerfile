FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/file-work-service ./cmd/file-work-service

FROM debian:trixie-slim AS pdf-tools
# Security updates stay enabled. Installed versions are recorded at build time;
# the worker requires qpdf JSON v2 (qpdf >= 11).
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates ghostscript qpdf util-linux \
    && dpkg-query -W ghostscript qpdf util-linux \
    && rm -rf /var/lib/apt/lists/*
FROM pdf-tools AS pdf-runtime
COPY --from=build /out/file-work-service /file-work-service
USER 65532:65532
EXPOSE 8007
ENTRYPOINT ["/file-work-service"]

FROM pdf-tools AS test
USER 0:0
RUN apt-get update && apt-get install -y --no-install-recommends \
    poppler-utils gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=build /usr/local/go /usr/local/go
COPY --from=build /src /src
COPY --from=build /go/pkg/mod /go/pkg/mod
ENV PATH="/usr/local/go/bin:${PATH}" GOCACHE=/tmp/go-cache GOMODCACHE=/go/pkg/mod
USER 65532:65532
ENTRYPOINT ["go"]
CMD ["test","-race","-p=2","./..."]

FROM pdf-runtime AS runtime
