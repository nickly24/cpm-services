FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /exams ./cmd/exams
FROM alpine:3.23
RUN adduser -D -u 10001 exams
COPY --from=build /exams /exams
USER 10001
EXPOSE 8010
ENTRYPOINT ["/exams"]
