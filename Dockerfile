FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /k8s-guardian ./cmd/k8s-guardian

FROM gcr.io/distroless/static:nonroot
COPY --from=build /k8s-guardian /usr/local/bin/k8s-guardian
USER nonroot
ENTRYPOINT ["/usr/local/bin/k8s-guardian"]
