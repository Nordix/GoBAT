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
	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// NewServer get the server implementation for the given protocol
func NewServer(ifNameAddressMap map[string]string, readBufferSize, port int, namespace, podName, nodeName, protocol string,
	config util.Config, reg *prometheus.Registry) []util.ServerImpl {
	servers := make([]util.ServerImpl, 0)
	switch protocol {
	case util.ProtocolUDP:
		for ifName, ip := range ifNameAddressMap {
			logrus.Infof("creating udp server for %s:%s", ifName, ip)
			udpServer, err := createUDPServer(namespace, podName, nodeName, ip, readBufferSize, port, config, reg)
			if err != nil {
				logrus.Errorf("error creating udp server on ip address %s: %v", ip, err)
				continue
			}
			servers = append(servers, udpServer)
		}
		return servers
	case util.ProtocolHTTP:
		logrus.Errorf("http server not supported")
		return nil
	default:
		logrus.Errorf("unknown protocol %s", protocol)
		return nil
	}
}

func createUDPServer(namespace, podName, nodeName, ipAddress string, readBufferSize, port int,
	config util.Config, reg *prometheus.Registry) (util.ServerImpl, error) {
	udpServer := NewUDPServer(namespace, podName, nodeName, ipAddress, port, reg)
	err := udpServer.SetupServerConnection(config)
	if err != nil {
		return nil, err
	}
	go udpServer.ReadFromSocket(readBufferSize)
	go udpServer.HandleIdleConnections(config)
	return udpServer, nil
}
