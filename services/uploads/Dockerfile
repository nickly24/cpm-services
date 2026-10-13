FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /uploads ./cmd/uploads
FROM alpine:3.23
RUN addgroup -g 10001 uploads && adduser -D -u 10001 -G uploads uploads
COPY --from=build /uploads /uploads
USER 10001:10001
EXPOSE 8014
ENTRYPOINT ["/uploads"]
