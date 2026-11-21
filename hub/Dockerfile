# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/hub ./cmd/hub
RUN mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hub /hub
# State (SQLite database and the hub signing key) lives in /data. A named
# volume inherits the nonroot ownership; for a bind mount, chown it to 65532.
COPY --from=build --chown=nonroot:nonroot /out/data /data
VOLUME /data
ENV HUB_ADDR=:8090 HUB_DB_PATH=/data/hub.db HUB_KEY_PATH=/data/hub.key
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/hub"]
CMD ["serve"]
