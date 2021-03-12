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
	"net"
	"strconv"
	"sync"

	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
)

// UDPServer udp server implementation
type UDPServer struct {
	Server          util.Server
	connection      *net.UDPConn
	isStopped       sync.WaitGroup
	msgHeaderLength int
	stop            bool
}

// NewUDPServer creates a new udp echo server
func NewUDPServer(ipAddress string, port int) util.ServerImpl {
	udpServer := &UDPServer{Server: util.Server{IPAddress: ipAddress, Port: port}, stop: false}
	udpServer.isStopped.Add(1)
	msgHeaderLength, err := util.GetMessageHeaderLength()
	if err != nil {
		panic(err)
	}
	udpServer.msgHeaderLength = msgHeaderLength
	return udpServer
}

// SetupServerConnection creates server socket and listens for incoming messages
func (s *UDPServer) SetupServerConnection(config util.Config) error {
	var serverAddress string
	if util.IsIPv6(s.Server.IPAddress) {
		serverAddress = "[" + s.Server.IPAddress + "]:" + strconv.Itoa(s.Server.Port)
	} else {
		serverAddress = s.Server.IPAddress + ":" + strconv.Itoa(s.Server.Port)
	}
	listeningAddress, err := net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return err
	}
	// Listen on all interfaces
	connection, err := net.ListenUDP("udp", listeningAddress)
	if err != nil {
		return err
	}
	// set read buffer size into 512KB
	connection.SetReadBuffer(512 * 1024)
	logrus.Infof("udp server listening on: %s", connection.LocalAddr())
	s.connection = connection
	return nil
}

// ReadFromSocket read packets from server socket and writes into the channel
func (s *UDPServer) ReadFromSocket(bufSize int) {
	logrus.Infof("tapp udp server read buffer size %d", bufSize)
	receivedByteArr := make([]byte, bufSize)
	for {
		size, addr, err := s.connection.ReadFromUDP(receivedByteArr)
		if err != nil {
			logrus.Errorf("error reading message from the udp server connection %v: err %v", s.connection, err)
			if s.stop == true {
				s.isStopped.Done()
				return
			}
			continue
		}
		if size > 0 {
			var msg util.Message
			err := msgpack.Unmarshal(receivedByteArr[:s.msgHeaderLength], &msg)
			if err != nil {
				logrus.Errorf("error in decoding the packet at udp server err %v", err)
				if s.stop == true {
					s.isStopped.Done()
					return
				}
				continue
			}
			// add response time to the message
			msg.RespondTimeStamp = util.GetTimestampMicroSec()
			byteArr, err := msgpack.Marshal(msg)
			copy(receivedByteArr, byteArr)
			if err != nil {
				logrus.Errorf("error in encoding the udp server response message %v", err)
				if s.stop == true {
					s.isStopped.Done()
					return
				}
				continue
			}
			//logrus.Infof("responding to messgage seq: %d, sendtimestamp: %d, respondtimestamp: %d", msg.SequenceNumber, msg.SendTimeStamp, msg.RespondTimeStamp)
			_, err = s.connection.WriteToUDP(receivedByteArr[:msg.Length], addr)
			if err != nil {
				logrus.Errorf("error in writing message %v back to udp client connection: err %v", msg, err)
				if s.stop == true {
					s.isStopped.Done()
					return
				}
				continue
			}
		}
		if s.stop == true {
			s.isStopped.Done()
			return
		}
	}
}

// TearDownServer stop the server
func (s *UDPServer) TearDownServer() {
	if s.connection == nil {
		return
	}
	s.stop = true
	s.connection.Close()
	s.isStopped.Wait()
	logrus.Infof("udp server connection %s is stopped", s.connection.LocalAddr().String())
}
