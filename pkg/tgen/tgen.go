// Copyright 2021 Ericsson Software Technology.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tgen

import (
	"errors"
	"fmt"

	"github.com/Nordix/GoBAT/pkg/util"
)

// NewClient get the client implementation for the given protocol
func NewClient(p *util.BatPair) (util.ClientImpl, error) {
	switch p.TrafficType {
	case util.ProtocolUDP:
		return NewUDPClient(p), nil
	case util.ProtocolHTTP:
		return nil, errors.New("http client not supported")
	default:
		return nil, fmt.Errorf("unknown protocol %s", p.TrafficType)
	}
}
