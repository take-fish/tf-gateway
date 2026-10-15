FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tf-gateway .

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/tf-gateway /app/tf-gateway
EXPOSE 8080
ENTRYPOINT ["/app/tf-gateway"]
