# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod .
RUN go mod download
COPY app/ ./app/
RUN CGO_ENABLED=0 GOOS=linux go build -o baristex ./app

# Runtime stage — docker:cli provides the Docker CLI needed for PDF generation
FROM docker:cli
WORKDIR /app
COPY --from=builder /build/baristex .
COPY www/ ./www/
EXPOSE 8080
CMD ["./baristex"]