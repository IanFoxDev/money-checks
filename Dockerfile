# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/money-checks ./cmd/money-checks

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/money-checks /money-checks
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/money-checks"]
CMD ["serve", "-c", "/etc/money-checks/checks.yaml"]
