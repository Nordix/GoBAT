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

package tapp

import (
	"errors"
	"fmt"

	"github.com/Nordix/GoBAT/pkg/util"
)

// NewServer get the server implementation for the given protocol
func NewServer(ifNameAddressMap map[string]string, readBufferSize, port int, protocol string, config util.Config) ([]util.ServerImpl, error) {
	servers := make([]util.ServerImpl, 0)
	switch protocol {
	case util.ProtocolUDP:
		for _, ip := range ifNameAddressMap {
			udpServer, err := createUDPServer(ip, readBufferSize, port, config)
			if err != nil {
				return nil, err
			}
			servers = append(servers, udpServer)
		}
		return servers, nil
	case util.ProtocolHTTP:
		return nil, errors.New("http server not supported")
	default:
		return nil, fmt.Errorf("unknown protocol %s", protocol)
	}
}

func createUDPServer(ipAddress string, readBufferSize, port int, config util.Config) (util.ServerImpl, error) {
	udpServer := NewUDPServer(ipAddress, port)
	err := udpServer.SetupServerConnection(config)
	if err != nil {
		return nil, err
	}
	go udpServer.ReadFromSocket(readBufferSize)
	return udpServer, nil
}
