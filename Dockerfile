FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /access-vw ./cmd/server/
RUN CGO_ENABLED=0 GOOS=linux go build -o /access-vw-init ./cmd/init/

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /access-vw /access-vw
COPY --from=builder /access-vw-init /access-vw-init
USER 65532:65532
ENTRYPOINT ["/access-vw"]
