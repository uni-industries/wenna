# wenna serve for self-hosting on a platform that terminates TLS (HOSTING.md).
# The database lives at /data: mount a persistent volume there.

# Base images are pinned to a minor (Go, matching go.mod) or Debian major.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum backend/
RUN cd backend && go mod download
COPY Makefile ./
COPY backend/ backend/
# The release workflow passes the tag; `wenna version` prints it.
ARG VERSION=dev
# The Makefile owns the build: tags, cgo, flags.
RUN make build VERSION="${VERSION}" OUT=/wenna

# distroless/base carries the glibc the cgo build links against. It runs as
# root: platform volumes mount root-owned, and a non-root user could not
# create the database on one.
FROM gcr.io/distroless/base-debian12
COPY --from=build /wenna /usr/local/bin/wenna
# Loopback is unreachable from outside a container, so the image listens on
# every interface and refuses to start until the deploy asserts
# WENNA_BEHIND_TLS_PROXY=true: the image cannot know what is in front of it.
ENV WENNA_DB=/data/wenna.db \
    WENNA_LISTEN_HOST=0.0.0.0
# No VOLUME instruction: Railway refuses to build an image that has one.
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/wenna"]
CMD ["serve"]
