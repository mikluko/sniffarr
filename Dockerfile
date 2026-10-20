FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags=-s -o /out/sniffarr .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/sniffarr /sniffarr
EXPOSE 8080 42069 42069/udp
ENTRYPOINT ["/sniffarr"]
