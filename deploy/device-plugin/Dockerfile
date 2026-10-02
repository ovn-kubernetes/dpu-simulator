FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
# Root go.mod replaces lib/dpusim with ./lib/dpusim; copy it before go mod download.
COPY lib/dpusim ./lib/dpusim/
RUN go mod download
# Only copy source needed by the plugin. Don't copy what is not needed to compile.
COPY cmd/dpu-sim-dp ./cmd/dpu-sim-dp/
COPY pkg ./pkg/
RUN CGO_ENABLED=0 GOOS=linux go build -o /dpu-sim-dp ./cmd/dpu-sim-dp

FROM gcr.io/distroless/static
COPY --from=builder /dpu-sim-dp /dpu-sim-dp
ENTRYPOINT ["/dpu-sim-dp"]
