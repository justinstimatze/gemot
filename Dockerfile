FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gemot .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /gemot /usr/local/bin/gemot
ENTRYPOINT ["gemot"]
CMD ["http", "--addr", ":8080"]
