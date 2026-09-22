# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/predictmaint ./cmd/predictmaint && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/simulator ./cmd/simulator

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/predictmaint /app/predictmaint
COPY --from=build /out/simulator /app/simulator
COPY migrations /app/migrations
ENV HTTP_ADDR=:8080 MIGRATIONS_DIR=/app/migrations
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/predictmaint"]
