#!/bin/bash

##############################################################################
# Copyright (C) 2021, Nordix Foundation
#
# All rights reserved. This program and the accompanying materials
# are made available under the terms of the Apache License, Version 2.0
# which accompanies this distribution, and is available at
# http://www.apache.org/licenses/LICENSE-2.0
##############################################################################

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
