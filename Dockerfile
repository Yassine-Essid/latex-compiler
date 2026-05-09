# --- Stage 1: Build the Go binary ---
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY *.go .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o latex-server .

# --- Stage 2: Runtime ---
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    texlive-base \
    texlive-latex-recommended \
    texlive-latex-extra \
    texlive-fonts-recommended \
    texlive-science \
    texlive-bibtex-extra \
    latexmk \
    librsvg2-bin \
    fontconfig \
    cabextract \
    xfonts-utils \
    ca-certificates \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN echo "ttf-mscorefonts-installer msttcorefonts/accepted-mscorefonts-eula select true" | debconf-set-selections \
    && apt-get update \
    && apt-get install -y --no-install-recommends ttf-mscorefonts-installer \
    && fc-cache -f -v \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/latex-server .
RUN mkdir -p /tmp/latex

EXPOSE 8000
CMD ["./latex-server"]
