# devtop — single image with the built React app, the Go API, and the
# CopilotKit AI runtime. Run against any repo with:
#
#   docker run --rm -it \
#     -v "$PWD:/workspace" \
#     -v devtop-ai-config:/etc/devtop \
#     -p 8000:8000 \
#     ghcr.io/synlace/devtop:latest

# Stage 1: build the React frontend
FROM node:22-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
COPY frontend/scripts ./scripts
RUN npm ci || npm install
COPY frontend/ .
RUN npm run build

# Stage 2: build the Go binary (serves the API + SPA + /api/copilotkit proxy)
FROM golang:1.25-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o devtop-bin .

# Stage 3: runtime — Go binary + React dist + Node for the CopilotKit runtime
FROM alpine:latest
RUN apk add --no-cache su-exec nodejs npm git ca-certificates

WORKDIR /app

COPY --from=backend /app/devtop-bin /app/devtop-bin
COPY --from=frontend /app/frontend/dist /app/frontend/dist
COPY frontend/package.json frontend/package-lock.json* /app/frontend/
COPY frontend/scripts /app/frontend/scripts/
COPY frontend/copilot-server.js frontend/persistent-runner.mjs /app/frontend/
RUN cd /app/frontend && (npm ci --omit=dev || npm install --omit=dev)

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8000
ENTRYPOINT ["/entrypoint.sh"]
