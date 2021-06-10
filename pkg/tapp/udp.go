// Copyright (C) 2021, Nordix Foundation
//
// All rights reserved. This program and the accompanying materials
// are made available under the terms of the Apache License, Version 2.0
// which accompanies this distribution, and is available at
// http://www.apache.org/licenses/LICENSE-2.0

package tapp

import (
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/Nordix/GoBAT/pkg/tgc"
	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
)

const (
	UDPProtocolStr = "udp"
	UDPServerPort  = 8890
)

const (
	activeClientStreamsStr = "active_client_streams"
	srvPacketsMissingStr   = "srv_packets_missing"
	srvPacketsLateStr      = "srv_packets_late"
)

// UDPServer udp server implementation
type UDPServer struct {
	Server              util.Server
	readBufferSize      int
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
func (s *UDPServer) SetupServerConnection() error {
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

func (s *UDPServer) registerMetric(metric prometheus.Collector) {
	s.promRegistry.MustRegister(metric)
	s.metrics = append(s.metrics, metric)
}

func (s *UDPServer) HandleIdleConnections() {
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
		for clientIp, clientInfo := range s.activeClientsMap {
			if currentTimeStamp > clientInfo.lastSeen+connectionTimeoutinMicros {
				delete(s.activeClientsMap, clientIp)
				s.activeClientStreams.Dec()
			}
		}
		s.mutex.Unlock()
		time.Sleep(sleepDuration)
	}
}

// ReadFromSocket read packets from server socket and writes into the channel
func (s *UDPServer) ReadFromSocket() {
	logrus.Infof("tapp udp server read buffer size %d", s.readBufferSize)
	receivedByteArr := make([]byte, s.readBufferSize)
	for {
		if s.stop {
			s.isStopped.Done()
			return
		}
		size, addr, err := s.connection.ReadFromUDP(receivedByteArr)
		if err != nil {
			logrus.Errorf("error reading message from the udp server connection %v: err %v", s.connection, err)
			continue
		}
		if size > 0 {
			var msg util.Message
			err := msgpack.Unmarshal(receivedByteArr[:s.msgHeaderLength], &msg)
			if err != nil {
				logrus.Errorf("error in decoding the packet at udp server err %v", err)
				continue
			}
			// add response time to the message
			msg.RespondTimeStamp = util.GetTimestampMicroSec()
			msg.ServerInfoLength = len(s.podInfoByteArr)
			byteArr, err := msgpack.Marshal(msg)
			copy(receivedByteArr, byteArr)
			copy(receivedByteArr[len(byteArr):], s.podInfoByteArr)
			if err != nil {
				logrus.Errorf("error in encoding the udp server response message %v", err)
				continue
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
			//logrus.Infof("responding to messgage seq: %d, sendtimestamp: %d, respondtimestamp: %d", msg.SequenceNumber, msg.SendTimeStamp, msg.RespondTimeStamp)
			_, err = s.connection.WriteToUDP(receivedByteArr[:msg.Length], addr)
			if err != nil {
				logrus.Errorf("error in writing message %v back to udp client connection: err %v", msg, err)
				continue
			}
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
	for _, metric := range s.metrics {
		s.promRegistry.Unregister(metric)
	}
	logrus.Infof("udp server connection %s is stopped", s.connection.LocalAddr().String())
}

type UDPProtoServerModule struct {
}

// CreateServer creates a new udp echo server
func (sm *UDPProtoServerModule) CreateServer(namespace, podName, nodeName, ipAddress, ifName string, readBufferSize int,
	reg *prometheus.Registry) (util.ServerImpl, error) {
	udpServer := sm.newUDPServer(namespace, podName, nodeName, ipAddress, readBufferSize, reg)
	err := udpServer.SetupServerConnection()
	if err != nil {
		return nil, err
	}
	go udpServer.ReadFromSocket()
	go udpServer.HandleIdleConnections()
	return udpServer, nil
}

// LoadBatProfileConfig update udp client with the given profile configuration
func (sm *UDPProtoServerModule) LoadBatProfileConfig(profileMap map[string]map[string]string) error {
	// no udp config needed for server
	return nil
}

func (sm *UDPProtoServerModule) newUDPServer(namespace, podName, workerName, ipAddress string, readBufferSize int,
	reg *prometheus.Registry) util.ServerImpl {
	udpServer := &UDPServer{Server: util.Server{PodInfo: util.PodInfo{Namespace: namespace, Name: podName, WorkerName: workerName},
		IPAddress: ipAddress, Port: UDPServerPort}, readBufferSize: readBufferSize, stop: false, mutex: &sync.Mutex{}}
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
	return udpServer
}

func init() {
	tgc.RegisterProtocolServer(UDPProtocolStr, &UDPProtoServerModule{})
}
