# Copyright (c) 2021 Ericsson Software Technology
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http:#www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM golang:1.15-alpine as builder
ADD . /usr/src/tgenapp
RUN apk add --update --virtual build-dependencies build-base linux-headers && \
    cd /usr/src/tgenapp && \
    make

FROM golang:1.15-alpine
RUN apk add --update \
    curl net-tools tcpdump bash \
    && rm -rf /var/cache/apk/*
COPY --from=builder /usr/src/tgenapp/bin/tgenapp /usr/bin/

CMD ["tgenapp"]
