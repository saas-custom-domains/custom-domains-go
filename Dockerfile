FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite is pure Go, so the binary needs no C libraries.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /snip . && mkdir /data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /snip /snip
COPY --from=build --chown=nonroot /data /data
ENV PORT=3000 DATA_DIR=/data
EXPOSE 3000
HEALTHCHECK --interval=2s --retries=30 CMD ["/snip", "healthcheck"]
ENTRYPOINT ["/snip"]
