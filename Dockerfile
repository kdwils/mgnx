FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o mgnx

FROM gcr.io/distroless/static-debian12

WORKDIR /app

COPY --chown=1000:1000 --from=builder /app/mgnx /app/

USER 1000

ENTRYPOINT ["/app/mgnx"]
CMD ["serve"]