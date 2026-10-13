FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /attendance ./cmd/attendance
FROM alpine:3.23
RUN adduser -D -u 10001 attendance
COPY --from=build /attendance /attendance
USER 10001
EXPOSE 8013
ENTRYPOINT ["/attendance"]
