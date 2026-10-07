FROM golang:alpine AS builder

ARG RELEASE=false
ARG COMPRESS=false
WORKDIR /openim-forks/open-im-server

RUN apk add --no-cache upx

RUN go install github.com/magefile/mage@v1.17.2

COPY protocol /openim-forks/protocol
COPY open-im-server /openim-forks/open-im-server
RUN go mod download
RUN RELEASE=${RELEASE} COMPRESS=${COMPRESS} mage build
RUN mage -compile ./mage -ldflags "-s -w"

FROM alpine:latest

WORKDIR /openim-server

COPY --from=builder /openim-forks/open-im-server/_output ./_output
COPY --from=builder /openim-forks/open-im-server/config ./config
COPY --from=builder /openim-forks/open-im-server/start-config.yml ./start-config.yml
COPY --from=builder /openim-forks/open-im-server/mage ./mage

ENTRYPOINT ["sh", "-c", "./mage start && sleep infinity"]
