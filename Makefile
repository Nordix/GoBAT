##############################################################################
# Copyright (C) 2021, Nordix Foundation
#
# All rights reserved. This program and the accompanying materials
# are made available under the terms of the Apache License, Version 2.0
# which accompanies this distribution, and is available at
# http://www.apache.org/licenses/LICENSE-2.0
##############################################################################

default :
	scripts/build.sh

image :
	scripts/build-image.sh

test :
	go test ./cmd/... ./pkg/...

vendor :
	go mod tidy && go mod vendor

clean :
	rm -rf $(CURDIR)/.gopath && rm -rf $(CURDIR)/bin && rm -rf $(CURDIR)/vendor
