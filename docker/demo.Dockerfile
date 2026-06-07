FROM golang:1.23-bookworm

ENV GOPROXY=https://goproxy.cn,direct

RUN apt-get update \
    && apt-get install -y --no-install-recommends curl iproute2 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
