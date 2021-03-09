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
	"net"

	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/sirupsen/logrus"
)

// NewServer get the server implementation for the given protocol
func NewServer(readBufferSize, port int, protocol string, config util.Config) ([]util.ServerImpl, error) {
	servers := make([]util.ServerImpl, 0)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	if len(ifaces) == 0 {
		return nil, errors.New("no interfaces present. server creation failed")
	}
	switch protocol {
	case util.ProtocolUDP:
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				logrus.Errorf("error in retrieving ip addess for interface %s: %v", iface.Name, err)
				continue
			}
			if addrs == nil || len(addrs) == 0 {
				logrus.Warnf("no ip addess assigned for interface %s, ignoring", iface.Name)
				continue
			}
			// There is always single IP assigned to the interface
			ipNet := addrs[0].(*net.IPNet)
			if ipNet.IP.IsLoopback() {
				logrus.Infof("loop back interface %s, ignoring", iface.Name)
				continue
			}
			udpServer, err := createUDPServer(ipNet.IP.String(), readBufferSize, port, config)
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
