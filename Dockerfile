# Stage 1: Build the Go app
FROM golang:1.25.6-alpine AS builder
RUN apk add --no-cache git bash sed findutils
WORKDIR /app

# Copy source and vendor typescript-go
COPY . .
RUN bash vendor-tsgo.sh
RUN go mod tidy

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux go build -o goodchanges .

# Stage 2: Run the app
FROM alpine:3.21
RUN apk add --no-cache git && git config --global --add safe.directory '*'

# Copy the built Go app binary
COPY --from=builder /app/goodchanges /usr/bin/goodchanges

ENTRYPOINT ["/usr/bin/goodchanges"]
