# 3-stage build: node builds the SPA → go builds a static binary embedding the
# SPA → tiny alpine runtime. Result is one self-contained binary serving
# /api, /mcp, and the kanban UI.

FROM node:22-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Use the freshly built SPA (overrides the committed placeholder) for go:embed.
COPY --from=web /web/dist ./web/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${VERSION}" -o /flightdeck ./cmd/flightdeck

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /flightdeck /usr/local/bin/flightdeck
EXPOSE 8080
ENTRYPOINT ["flightdeck", "serve"]
