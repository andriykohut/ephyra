# syntax=docker/dockerfile:1

# --- build the frontend ---
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- build the binary, with the frontend embedded ---
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./web/dist
ARG VERSION=dev
# TARGETOS/TARGETARCH come from buildx. The binary is pure Go, so a native
# cross-compile beats emulating the whole toolchain under QEMU.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X github.com/andriykohut/ephyra/internal/buildinfo.version=${VERSION}" \
    -o /ephyra ./cmd/ephyra
# /data is where STORE_PATH and WORK_DIR live; make it writable by the nonroot
# user so a fresh volume inherits that ownership.
RUN mkdir -p /data && chown -R 65532:65532 /data

# --- ship it ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ephyra /ephyra
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/ephyra"]
