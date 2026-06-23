ARG BASE_IMAGE=latex-base:latest

# --- Stage 1: Build the Go binary ---
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o latex-server .

# --- Stage 2: Runtime (based on pre-built TeX base image) ---
FROM ${BASE_IMAGE}

COPY --from=builder /app/latex-server .

EXPOSE 8000
CMD ["./latex-server"]
