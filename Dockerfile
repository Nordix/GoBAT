##############################################################################
# Copyright (C) 2021, Nordix Foundation
#
# All rights reserved. This program and the accompanying materials
# are made available under the terms of the Apache License, Version 2.0
# which accompanies this distribution, and is available at
# http://www.apache.org/licenses/LICENSE-2.0
##############################################################################

FROM golang:1.15-alpine as builder
ADD . /usr/src/tgenapp
RUN apk add --update --virtual build-dependencies build-base linux-headers git && \
    cd /usr/src/tgenapp && \
    make

FROM alpine:3.11
RUN apk add --update \
    curl net-tools tcpdump bash \
    && rm -rf /var/cache/apk/*
COPY --from=builder /usr/src/tgenapp/bin/tgenapp /usr/bin/

CMD ["tgenapp"]
