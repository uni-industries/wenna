# wenna serve on Cloudflare Containers (HOSTING.md § Cloudflare Containers).
# The container's disk is erased whenever it sleeps, so Litestream runs wenna:
# it restores the database from the user's R2 bucket when /data has none,
# streams every change back, and syncs once more when wenna exits.
# Built by wrangler from the repo root (wrangler.jsonc image_build_context).

# Base images are pinned to a minor (Go, matching go.mod), a Debian major, or
# Litestream's 0.x minor.
FROM litestream/litestream:0.5 AS litestream

# The same build as ../Dockerfile.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum backend/
RUN cd backend && go mod download
COPY Makefile ./
COPY backend/ backend/
ARG VERSION=dev
RUN make build VERSION="${VERSION}" OUT=/wenna
# wenna refuses a WENNA_DB whose directory is missing; this disk has no volume.
RUN mkdir /data

FROM gcr.io/distroless/base-debian12
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY --from=build /wenna /usr/local/bin/wenna
COPY --from=build /data /data
COPY cloudflare/litestream.yml /etc/litestream.yml
ENV WENNA_DB=/data/wenna.db \
    WENNA_LISTEN_HOST=0.0.0.0
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/litestream"]
CMD ["replicate", "-restore-if-db-not-exists", "-exec", "/usr/local/bin/wenna serve"]
