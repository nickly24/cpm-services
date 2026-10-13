FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bff ./cmd/bff
FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /bff /bff
USER 65532:65532
ENV BFF_DOCKER=1
EXPOSE 8001
ENTRYPOINT ["/bff"]
