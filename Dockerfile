# Build stage
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o symphony ./cmd/symphony

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache bash ca-certificates git

# Create non-root user
RUN adduser -D -s /bin/bash symphony
USER symphony

WORKDIR /home/symphony

COPY --from=builder /app/symphony /usr/local/bin/symphony

# Default workspace directory
RUN mkdir -p /home/symphony/workspaces

ENTRYPOINT ["symphony"]
CMD ["--workflow", "WORKFLOW.md"]
