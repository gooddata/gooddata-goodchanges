# Stage 1: Build the Go app
FROM 020413372491.dkr.ecr.us-east-1.amazonaws.com/pullthrough/docker.io/library/golang:1.26.0-alpine AS builder
RUN apk add --no-cache git bash sed findutils
WORKDIR /app

# Copy source and vendor typescript-go
COPY . .
RUN bash vendor-tsgo.sh
RUN go mod tidy

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux go build -o goodchanges .

# Stage 2: Run the app
FROM 020413372491.dkr.ecr.us-east-1.amazonaws.com/pullthrough/docker.io/library/alpine:3.23
RUN apk add --no-cache git && git config --global --add safe.directory '*'

# Copy the built Go app binary
COPY --from=builder /app/goodchanges /usr/bin/goodchanges

ENTRYPOINT ["/usr/bin/goodchanges"]
