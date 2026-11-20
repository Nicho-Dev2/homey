# OneForArr image: the web interface is built, then built into one Go
# program, which runs on an empty base image (no shell, no package manager).
# Build from the oneforarr folder:  docker build -f deploy/Dockerfile -t oneforarr .

# 1. Web interface (React + TypeScript)
FROM node:22-alpine AS ui
WORKDIR /src/app/frontend
COPY app/frontend/package.json app/frontend/package-lock.json* ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY app/frontend/ ./
RUN npm run build

# 2. Program (Go), with the web interface inside it
FROM golang:1.26-alpine AS app
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY app/ app/
COPY --from=ui /src/app/frontend/dist/ app/frontend/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/oneforarr ./app/backend/cmd/oneforarr

# 3. Final image: the program and the certificates it needs for HTTPS
FROM scratch
COPY --from=app /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=app /out/oneforarr /oneforarr
ENV ONEFORARR_CONFIG_DIR=/config ONEFORARR_PORT=7979
VOLUME /config
EXPOSE 7979
# Runs as "nobody" unless docker compose sets user: (see README).
USER 65534:65534
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/oneforarr", "healthcheck"]
ENTRYPOINT ["/oneforarr"]
