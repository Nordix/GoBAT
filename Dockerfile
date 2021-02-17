FROM golang:1.13-alpine as builder
ADD . /usr/src/tgenapp
RUN apk add --update --virtual build-dependencies build-base linux-headers && \
    cd /usr/src/tgenapp && \
    make

FROM golang:1.13-alpine
COPY --from=builder /usr/src/tgenapp/bin/tgenapp /usr/bin/

CMD ["tgenapp"]
