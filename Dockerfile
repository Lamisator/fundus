# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/Lamisator/fundus/internal/server.Version=${VERSION}" \
      -o /out/fundus ./cmd/fundus

# distroless: CA certificates (for the OIDC issuer), tzdata, no shell.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fundus /fundus
ENV FUNDUS_ADDR=:8080 \
    FUNDUS_DATA_DIR=/data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/fundus", "-healthcheck", ":8080"]
ENTRYPOINT ["/fundus"]
