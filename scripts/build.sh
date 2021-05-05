##############################################################################
# Copyright (C) 2021, Nordix Foundation
#
# All rights reserved. This program and the accompanying materials
# are made available under the terms of the Apache License, Version 2.0
# which accompanies this distribution, and is available at
# http://www.apache.org/licenses/LICENSE-2.0
##############################################################################

set -e

ORG_PATH="github.com/Nordix"
REPO_PATH="${ORG_PATH}/GoBAT"

if [ ! -h .gopath/src/${REPO_PATH} ]; then
	mkdir -p .gopath/src/${ORG_PATH}
	ln -s ../../../.. .gopath/src/${REPO_PATH} || exit 255
fi

export GOPATH=${PWD}/.gopath
export GOBIN=${PWD}/bin
export CGO_ENABLED=0
export GO15VENDOREXPERIMENT=1
export REV_LIST=`git rev-list --tags --max-count=1`
export REVISION=`git describe --tags $REV_LIST`
export COMMIT_ID=`git rev-parse --short HEAD`

go install -ldflags "-X main.tgentappver=$REVISION-$COMMIT_ID" -tags no_openssl "$@" ${REPO_PATH}/cmd/tgenapp
 
