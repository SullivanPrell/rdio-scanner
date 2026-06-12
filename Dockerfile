##############################################################################
# Rdio Scanner — multi-stage build
# Stage 1: Build Angular client (Node.js)
# Stage 2: Build Go server (embeds compiled webapp)
# Stage 3: Minimal Alpine runtime
##############################################################################

# ── Stage 1: Angular client ───────────────────────────────────────────────
FROM node:22-alpine AS client-builder
WORKDIR /client
COPY client/package*.json ./
RUN npm ci --loglevel=error --no-progress
COPY client/ ./
RUN npm run build

# ── Stage 2: Go server ───────────────────────────────────────────────────
FROM golang:1.24-alpine AS server-builder
WORKDIR /server
# Copy pre-built Angular output into the expected embed location
COPY --from=client-builder /client/dist/rdio-scanner/ /server/webapp/
COPY server/ ./
RUN go build -o /rdio-scanner

# ── Stage 3: Runtime ─────────────────────────────────────────────────────
FROM alpine:latest
LABEL maintainer="rdio-scanner"
WORKDIR /app

RUN apk --no-cache add ffmpeg mailcap tzdata ca-certificates

COPY --from=server-builder /rdio-scanner ./rdio-scanner

RUN mkdir -p /app/data

VOLUME ["/app/data"]
EXPOSE 3000

ENV DOCKER=1

ENTRYPOINT ["./rdio-scanner", "-base_dir", "/app/data"]
