FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/awxnext ./cmd/awxnext

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/awxnext /usr/local/bin/awxnext
EXPOSE 8052
ENTRYPOINT ["/usr/local/bin/awxnext"]
CMD ["serve"]
