// Copyright (c) 2021 Nordix Foundation.
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

// Package tapp contains different protocol stream server implementations
package tapp

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"

	"github.com/Nordix/GoBAT/pkg/tgc"
	"github.com/Nordix/GoBAT/pkg/util"
)

const (
	// UDPProtocolStr udp protocol string
	UDPProtocolStr = "udp"
	// UDPServerPort udp server port number
	UDPServerPort = 8890
)

const (
	activeClientStreamsStr = "active_client_streams"
	srvPacketsMissingStr   = "srv_packets_missing"
	srvPacketsLateStr      = "srv_packets_late"
)

const (
	// DefaultUDPPacketSize default udp packet size
	DefaultUDPPacketSize = 1000
	// DefaultUDPPacketTimeout default udp packet timeout
	DefaultUDPPacketTimeout = 5
	// DefaultUDPSendRate default udp send rate
	DefaultUDPSendRate = 500
	// DefaultUDPRedialTimeout default udp redial timeout
	DefaultUDPRedialTimeout = 5
	// DefaultSocketReadBufSize default udp socket read buffer size
	DefaultSocketReadBufSize = 512 * 1024
)

type udpServer struct {
	Server              util.Server
	conf                *config
	podInfoByteArr      []byte
	connection          *net.UDPConn
	isStopped           sync.WaitGroup
	msgHeaderLength     int
	stop                bool
	activeClientStreams prometheus.Gauge
	packetsMissing      prometheus.Counter
	packetsLate         prometheus.Counter
	promRegistry        *prometheus.Registry
	metrics             []prometheus.Collector
	activeClientsMap    map[string]*clientInfo
	mutex               *sync.Mutex
}

type clientInfo struct {
	lastSeen      int64
	arrivedMaxSeq int64
}

// SetupServerConnection creates server socket and listens for incoming messages
func (s *udpServer) SetupServerConnection() error {
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

	err = connection.SetReadBuffer(s.conf.socketReadBufSize)
	if err != nil {
		return err
	}
	logrus.Infof("udp server listening on: %s", connection.LocalAddr())
	s.connection = connection

	labelMap := make(map[string]string)
	labelMap["server_ip"] = s.Server.IPAddress
	labelMap["server_namespace"] = s.Server.PodInfo.Namespace
	labelMap["server_name"] = s.Server.PodInfo.Name
	labelMap["worker_name"] = s.Server.PodInfo.WorkerName

	s.activeClientStreams = util.NewGauge(util.PromNamespace, UDPProtocolStr, activeClientStreamsStr, "active client streams", labelMap)
	s.registerMetric(s.activeClientStreams)

	s.packetsMissing = util.NewCounter(util.PromNamespace, UDPProtocolStr, srvPacketsMissingStr, "packets missing or arrive later", labelMap)
	s.registerMetric(s.packetsMissing)

	s.packetsLate = util.NewCounter(util.PromNamespace, UDPProtocolStr, srvPacketsLateStr, "packets late", labelMap)
	s.registerMetric(s.packetsLate)

	s.activeClientsMap = make(map[string]*clientInfo)
	return nil
}

func (s *udpServer) registerMetric(metric prometheus.Collector) {
	s.promRegistry.MustRegister(metric)
	s.metrics = append(s.metrics, metric)
}

// HandleIdleConnections monitors idle client connections and update activeClientStreams metric
func (s *udpServer) HandleIdleConnections() {
	// use 60s for connection timeout
	connectionTimeout := 60
	sleepDuration := time.Duration(int64((float64(10) / float64(connectionTimeout)) * float64(time.Second)))
	connectionTimeoutinMicros := int64(util.SecToMicroSec(connectionTimeout))
	for {
		if s.stop {
			s.isStopped.Done()
			return
		}
		currentTimeStamp := util.GetTimestampMicroSec()
		s.mutex.Lock()
		for clientIP, clientInfo := range s.activeClientsMap {
			if currentTimeStamp > clientInfo.lastSeen+connectionTimeoutinMicros {
				delete(s.activeClientsMap, clientIP)
				s.activeClientStreams.Dec()
			}
		}
		s.mutex.Unlock()
		time.Sleep(sleepDuration)
	}
}

// ReadFromSocket read packets from server socket and writes into the channel
func (s *udpServer) ReadFromSocket() {
	readBufSize := s.msgHeaderLength + s.conf.packetSize
	logrus.Infof("tapp udp server receive buffer size %d", readBufSize)
	readByteArr := make([]byte, readBufSize)
	for {
		if s.stop {
			s.isStopped.Done()
			return
		}
		size, addr, err := s.connection.ReadFromUDP(readByteArr)
		if err != nil {
			logrus.Errorf("error reading message from the udp server connection %v: err %v", s.connection, err)
			continue
		}
		if size > 0 {
			var msg util.Message
			err := msgpack.Unmarshal(readByteArr[:s.msgHeaderLength], &msg)
			if err != nil {
				logrus.Errorf("error in decoding the packet at udp server err %v", err)
				continue
			}
			// add response time to the message
			msg.RespondTimeStamp = util.GetTimestampMicroSec()
			msg.ServerInfoLength = len(s.podInfoByteArr)
			byteArr, err := msgpack.Marshal(msg)
			if err != nil {
				logrus.Errorf("error in encoding the udp server response message %v", err)
				continue
			}
			copy(readByteArr, byteArr)
			copy(readByteArr[len(byteArr):], s.podInfoByteArr)
			pktLength := len(byteArr) + len(s.podInfoByteArr)
			if msg.Length < pktLength {
				msg.Length = pktLength
			}
			clientIP := addr.IP.String()
			s.mutex.Lock()
			if _, exists := s.activeClientsMap[clientIP]; !exists {
				s.activeClientStreams.Inc()
				s.activeClientsMap[clientIP] = &clientInfo{}
			}
			s.activeClientsMap[clientIP].lastSeen = util.GetTimestampMicroSec()
			if msg.SequenceNumber > s.activeClientsMap[clientIP].arrivedMaxSeq {
				seqDiff := msg.SequenceNumber - s.activeClientsMap[clientIP].arrivedMaxSeq - 1
				if seqDiff > 0 {
					s.packetsMissing.Add(float64(seqDiff))
				}
				s.activeClientsMap[clientIP].arrivedMaxSeq = msg.SequenceNumber
			} else {
				s.packetsLate.Inc()
			}
			s.mutex.Unlock()
			// logrus.Infof("responding to messgage seq: %d, sendtimestamp: %d, respondtimestamp: %d", msg.SequenceNumber, msg.SendTimeStamp, msg.RespondTimeStamp)
			_, err = s.connection.WriteToUDP(readByteArr[:msg.Length], addr)
			if err != nil {
				logrus.Errorf("error in writing message %v back to udp client connection: err %v", msg, err)
				continue
			}
		}
	}
}

// TearDownServer stop the server
func (s *udpServer) TearDownServer() {
	if s.connection == nil {
		return
	}
	s.stop = true
	err := s.connection.Close()
	if err != nil {
		logrus.Warnf("error closing udp server connection %s: %v", s.connection.LocalAddr().String(), err)
	}
	s.isStopped.Wait()
	for _, metric := range s.metrics {
		s.promRegistry.Unregister(metric)
	}
	logrus.Infof("udp server connection %s is stopped", s.connection.LocalAddr().String())
}

type udpProtoServerModule struct {
	conf *config
}

type config struct {
	socketReadBufSize int
	packetSize        int
}

// CreateServer creates a new udp echo server
func (sm *udpProtoServerModule) CreateServer(namespace, podName, nodeName, ipAddress, ifName string,
	reg *prometheus.Registry) (util.ServerImpl, error) {
	udpServer := sm.newUDPServer(namespace, podName, nodeName, ipAddress, reg)
	err := udpServer.SetupServerConnection()
	if err != nil {
		return nil, err
	}
	go udpServer.ReadFromSocket()
	go udpServer.HandleIdleConnections()
	return udpServer, nil
}

// LoadBatProfileConfig update udp client with the given profile configuration
func (sm *udpProtoServerModule) LoadBatProfileConfig(profileMap map[string]map[string]string) error {
	if profileMap == nil {
		return errors.New("error parsing the udp profile config map data")
	}
	var err error

	// Parse UDP profile
	if udpEntry, ok := profileMap[UDPProtocolStr]; ok {
		if val, ok := udpEntry["socket-read-buf-size"]; ok {
			sm.conf.socketReadBufSize, err = sm.parseIntValue(val)
			if err != nil {
				return fmt.Errorf("parsing udp-socket-read-buf-size failed: err %v", err)
			}
		}

		if val, ok := udpEntry["packet-size"]; ok {
			sm.conf.packetSize, err = sm.parseIntValue(val)
			if err != nil {
				return fmt.Errorf("parsing udp-packet-size failed: err %v", err)
			}
		}
	}

	logrus.Infof("udp server profiling config: %v", *sm.conf)

	return nil
}

func (sm *udpProtoServerModule) parseIntValue(value string) (int, error) {
	valueInt, err := strconv.Atoi(value)
	if err != nil {
		return -1, err
	}
	return valueInt, nil
}

func (sm *udpProtoServerModule) newUDPServer(namespace, podName, workerName, ipAddress string, reg *prometheus.Registry) util.ServerImpl {
	udpServer := &udpServer{Server: util.Server{PodInfo: util.PodInfo{Namespace: namespace, Name: podName, WorkerName: workerName},
		IPAddress: ipAddress, Port: UDPServerPort}, stop: false, mutex: &sync.Mutex{}}
	udpServer.isStopped.Add(2)
	msgHeaderLength, err := util.GetMessageHeaderLength()
	if err != nil {
		panic(err)
	}
	podInfoByteArr, err := msgpack.Marshal(udpServer.Server.PodInfo)
	if err != nil {
		panic(err)
	}
	udpServer.msgHeaderLength = msgHeaderLength
	udpServer.podInfoByteArr = podInfoByteArr
	udpServer.promRegistry = reg
	udpServer.metrics = make([]prometheus.Collector, 0)
	udpServer.conf = sm.conf
	return udpServer
}

func init() {
	tgc.RegisterProtocolServer(UDPProtocolStr, &udpProtoServerModule{&config{socketReadBufSize: DefaultSocketReadBufSize,
		packetSize: DefaultUDPPacketSize}})
}
