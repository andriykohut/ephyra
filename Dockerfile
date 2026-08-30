# syntax=docker/dockerfile:1

# --- build the frontend ---
FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- build the binary, with the frontend embedded ---
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/andrii/ephyra/internal/buildinfo.version=${VERSION}" \
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
