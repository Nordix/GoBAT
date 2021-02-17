#!/bin/bash

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

set -e

# set proxy args
BUILD_ARGS=()
[ ! -z "$http_proxy"  ] && BUILD_ARGS+=("--build-arg http_proxy=$http_proxy")
[ ! -z "$HTTP_PROXY"  ] && BUILD_ARGS+=("--build-arg HTTP_PROXY=$HTTP_PROXY")
[ ! -z "$https_proxy" ] && BUILD_ARGS+=("--build-arg https_proxy=$https_proxy")
[ ! -z "$HTTPS_PROXY" ] && BUILD_ARGS+=("--build-arg HTTPS_PROXY=$HTTPS_PROXY")
[ ! -z "$no_proxy"    ] && BUILD_ARGS+=("--build-arg no_proxy=$no_proxy")
[ ! -z "$NO_PROXY"    ] && BUILD_ARGS+=("--build-arg NO_PROXY=$NO_PROXY")

# build tgen-tapp Docker image
docker build ${BUILD_ARGS[@]} -f Dockerfile -t tgen-tapp .
