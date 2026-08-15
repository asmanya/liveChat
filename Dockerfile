# Build stage — compiles the server into a single static binary.
FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /livechat .

# Run stage — just the binary and the static assets it serves, nothing else.
FROM alpine:3.20
WORKDIR /app

COPY --from=build /livechat ./livechat
COPY static ./static

ENV DATA_DIR=/data
EXPOSE 8080

CMD ["./livechat"]
