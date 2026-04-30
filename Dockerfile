FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gemot .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /gemot /usr/local/bin/gemot

# `docker run gemot/gemot` works out of the box: with no DATABASE_URL set,
# gemot boots in demo mode (in-memory store, ephemeral, no auth required)
# so a curious dev can connect an MCP client and start using it. For
# persistent state, supply DATABASE_URL pointing at a Postgres instance.
EXPOSE 8080
ENTRYPOINT ["gemot"]
CMD ["http", "--addr", ":8080"]
