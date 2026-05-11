# Dev container for `make dev` / docker-compose. Bookworm (Debian 12,
# glibc 2.36) — must match build/linux.Dockerfile, because the project
# only supports glibc Linux (the DuckDB driver pulls in glibc-specific
# symbols like __res_init that musl doesn't shim). Using alpine here
# would compile fine on its own but produce a binary whose API/UI
# disagree with the production target.
FROM golang:1.26-bookworm

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        bash make build-essential ca-certificates git && \
    rm -rf /var/lib/apt/lists/*

# Hot reload for Go (rebuilds + restarts on file change). Pinned: a
# floating @latest is a supply-chain and reproducibility hole.
RUN go install github.com/air-verse/air@v1.62.0

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# The repo is bind-mounted at /app in docker-compose so source changes
# are picked up live. `air` reads .air.toml from the workdir.
CMD ["air"]
