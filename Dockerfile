FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION COMMIT BUILD_TIME
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/sniffarr \
    -ldflags="-s \
    -X sarak.sh/mikluko/libarr/version.version=$VERSION \
    -X sarak.sh/mikluko/libarr/version.commit=$COMMIT \
    -X sarak.sh/mikluko/libarr/version.built=$BUILD_TIME" .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/sniffarr /sniffarr
EXPOSE 8080 42069 42069/udp
ENTRYPOINT ["/sniffarr"]
