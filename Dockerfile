FROM golang:1.26.5-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags='-s -w' -o /knowledgehub ./cmd/knowledgehub

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /knowledgehub /usr/local/bin/knowledgehub
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["knowledgehub"]
CMD ["serve"]
