# --- Stage 1: Build the Go Binary ---
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod ./
# Copy source code
COPY main.go .

# Download dependencies (if you had a go.sum, you'd copy it here too)
RUN go mod tidy

# Build the binary. 
# CGO_ENABLED=0 ensures a static binary that runs anywhere.
RUN CGO_ENABLED=0 GOOS=linux go build -o latex-server main.go

# --- Stage 2: Create the Runtime Image ---
FROM ubuntu:22.04

# Avoid interactive prompts
ENV DEBIAN_FRONTEND=noninteractive

# 1. Install minimal TeX Live packages
# Replaced texlive-full with specific packages to reduce image size
RUN apt-get update && apt-get install -y --no-install-recommends \
    texlive-base \
    texlive-latex-recommended \
    texlive-latex-extra \
    texlive-fonts-recommended \
    latexmk \
    fontconfig \
    cabextract \
    xfonts-utils \
    ca-certificates \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# 2. Install Microsoft Core Fonts
RUN echo "ttf-mscorefonts-installer msttcorefonts/accepted-mscorefonts-eula select true" | debconf-set-selections \
    && apt-get update \
    && apt-get install -y --no-install-recommends ttf-mscorefonts-installer \
    && fc-cache -f -v \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 3. Copy the compiled Go binary from the builder stage
COPY --from=builder /app/latex-server .

# 4. Create temp directory
RUN mkdir -p /tmp/latex

EXPOSE 8000

# 5. Run the binary
CMD ["./latex-server"]