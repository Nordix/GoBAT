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

// Package tgen contains different protocol stream client implementations
package tgen

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"

	"github.com/Nordix/GoBAT/pkg/tapp"
	"github.com/Nordix/GoBAT/pkg/tgc"
	"github.com/Nordix/GoBAT/pkg/util"
)

const (
	// trafficNotStarted represents traffic not started
	trafficNotStartedStr = "traffic_not_started"
	// packetSent represents total packets sent
	packetSentStr = "packets_sent"
	// packetSendFailed represents send failed packets
	packetSendFailedStr = "packet_send_failed"
	// packetReceived represents total packets received
	packetReceivedStr = "packets_received"
	// packetDropped represents total packets received
	packetDroppedStr = "packets_dropped"
	// roundTripTime represent total round trip time
	roundTripTimeStr = "total_round_trip_time"
	// latency represent latency summary
	latencyStr = "latency"
	// serverNamespaceStr pod namespace key used in the metric label map
	serverNamespaceStr = "server_namespace"
	// serverPodStr pod key used in the metric label map
	serverPodStr = "server_pod"
	// serverNodeStr node key used in the metric label map
	serverNodeStr = "server_node"
)

const (
	defaultUDPPacketSize    = 1000
	defaultUDPPacketTimeout = 5
	defaultUDPSendRate      = 500
	defaultUDPRedialTimeout = 5
)

type udpStream struct {
	isStopped         sync.WaitGroup
	connection        *net.UDPConn
	localAddr         *net.UDPAddr
	pair              *util.BatPair
	conf              *config
	readBufferSize    int
	packetSequence    int64
	mutex             *sync.Mutex
	msgHeaderLength   int
	stop              bool
	promRegistry      *prometheus.Registry
	streamMetrics     []prometheus.Collector
	metricLabelMap    map[string]string
	trafficNotStarted prometheus.Counter
	packetSent        prometheus.Counter
	packetSendFailed  prometheus.Counter
	packetReceived    prometheus.Counter
	packetDropped     prometheus.Counter
	roundTrip         prometheus.Counter
	latency           prometheus.Summary
}

// SetupConnection sets up udp client connection
func (c *udpStream) SetupConnection() error {
	c.metricLabelMap = make(map[string]string)
	c.metricLabelMap["destination"] = c.pair.Destination.Name
	c.metricLabelMap["scenario"] = c.pair.TrafficScenario
	c.metricLabelMap["packet_size"] = strconv.Itoa(c.conf.packetSize)
	c.metricLabelMap["packet_rate"] = strconv.Itoa(c.conf.sendRate)
	source, _ := json.Marshal(c.pair.Source)
	c.metricLabelMap["source"] = string(source)
	c.trafficNotStarted = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, trafficNotStartedStr, "traffic not started", c.metricLabelMap)
	c.promRegistry.MustRegister(c.trafficNotStarted)

	var destAddress string
	if util.IsIPv6(c.pair.Destination.Name) {
		destAddress = "[" + c.pair.Destination.Name + "]:" + strconv.Itoa(tapp.UDPServerPort)
	} else {
		if !util.IsIPv4(c.pair.Destination.Name) {
			// destination is domain name
			c.pair.Destination.IsDN = true
		}
		destAddress = c.pair.Destination.Name + ":" + strconv.Itoa(tapp.UDPServerPort)
	}
	raddr, err := net.ResolveUDPAddr("udp", destAddress)
	if err != nil {
		c.trafficNotStarted.Inc()
		return err
	}
	c.pair.Destination.IP = raddr.IP.String()
	var srcAddress string
	if util.IsIPv6(c.pair.Source.IP) {
		srcAddress = "[" + c.pair.Source.IP + "]:0"
	} else {
		srcAddress = c.pair.Source.IP + ":0"
	}
	laddr, err := net.ResolveUDPAddr("udp", srcAddress)
	if err != nil {
		c.trafficNotStarted.Inc()
		return err
	}
	logrus.Infof("udp local address: %s, server address: %s connecting ", laddr.String(), c.pair.Destination.IP)
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		c.trafficNotStarted.Inc()
		return err
	}
	// set read buffer size into 512KB
	err = conn.SetReadBuffer(512 * 1024)
	if err != nil {
		return err
	}
	c.connection = conn
	c.localAddr = laddr

	c.metricLabelMap[serverNamespaceStr] = ""
	c.metricLabelMap[serverPodStr] = ""
	c.metricLabelMap[serverNodeStr] = ""
	c.registerStreamMetrics()

	return nil
}

func (c *udpStream) registerMetric(metric prometheus.Collector) {
	c.promRegistry.MustRegister(metric)
	c.streamMetrics = append(c.streamMetrics, metric)
}

func (c *udpStream) registerStreamMetrics() {
	c.packetSent = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetSentStr, "total packet sent", c.metricLabelMap)
	c.registerMetric(c.packetSent)

	c.packetSendFailed = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetSendFailedStr, "total packet send failed", c.metricLabelMap)
	c.registerMetric(c.packetSendFailed)

	c.packetReceived = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetReceivedStr, "total packet received", c.metricLabelMap)
	c.registerMetric(c.packetReceived)

	c.packetDropped = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetDroppedStr, "total packet dropped", c.metricLabelMap)
	c.registerMetric(c.packetDropped)

	c.roundTrip = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, roundTripTimeStr, "total round trip time", c.metricLabelMap)
	c.registerMetric(c.roundTrip)

	objectives := map[float64]float64{0.5: 0.05, 0.9: 0.02, 0.95: 0.01, 0.99: 0.005}
	c.latency = util.NewSummary(util.PromNamespace, c.pair.TrafficProfile, latencyStr, "latency statistics", c.metricLabelMap, objectives)
	c.registerMetric(c.latency)
}

func (c *udpStream) deRegisterStreamMetrics() {
	for _, metric := range c.streamMetrics {
		c.promRegistry.Unregister(metric)
	}
}

func (c *udpStream) reRegisterStreamMetrics() {
	c.deRegisterStreamMetrics()
	c.registerStreamMetrics()
}

// SocketRead read from udp client socket
func (c *udpStream) SocketRead() {
	logrus.Infof("udp tgen client read buffer size %d", c.readBufferSize)
	receivedByteArr := make([]byte, c.readBufferSize)
	for {
		if c.stop {
			c.isStopped.Done()
			return
		}
		size, _, err := c.connection.ReadFromUDP(receivedByteArr)
		if err != nil {
			logrus.Debugf("error reading message from the udp client connection %v: err %v", c.connection, err)
			continue
		}
		if size > 0 {
			var (
				msg        util.Message
				serverInfo util.PodInfo
			)
			err := msgpack.Unmarshal(receivedByteArr[:c.msgHeaderLength], &msg)
			err1 := msgpack.Unmarshal(receivedByteArr[c.msgHeaderLength:c.msgHeaderLength+msg.ServerInfoLength], &serverInfo)
			if err != nil || err1 != nil {
				logrus.Errorf("error in decoding the packet at udp client err %v: %v", err, err1)
				continue
			}
			// logrus.Infof("%s-%s: message received seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
			c.mutex.Lock()
			_, exists := c.pair.PendingRequestsMap[msg.SequenceNumber]
			if !exists {
				c.mutex.Unlock()
				// msg already timed out
				// logrus.Infof("%s-%s: ignoring message seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
				continue
			}
			// logrus.Infof("%s-%s: processing message seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
			roundTripTime := float64(util.GetTimestampMicroSec() - msg.SendTimeStamp)
			c.roundTrip.Add(roundTripTime)
			c.latency.Observe(roundTripTime)
			c.packetReceived.Inc()
			delete(c.pair.PendingRequestsMap, msg.SequenceNumber)
			// if there is change in server namespace, pod and its hosted worker name, re register the stream metrics
			if serverInfo.Namespace != c.metricLabelMap[serverNamespaceStr] ||
				serverInfo.Name != c.metricLabelMap[serverPodStr] ||
				serverInfo.WorkerName != c.metricLabelMap[serverNodeStr] {
				c.metricLabelMap[serverNamespaceStr] = serverInfo.Namespace
				c.metricLabelMap[serverPodStr] = serverInfo.Name
				c.metricLabelMap[serverNodeStr] = serverInfo.WorkerName
				c.reRegisterStreamMetrics()
			}
			c.mutex.Unlock()
		}
	}
}

// HandleTimeouts handles the message timeouts
func (c *udpStream) HandleTimeouts() {
	sleepDuration := time.Duration(int64((float64(2.5) / float64(c.conf.packetTimeout)) * float64(time.Second)))
	packetTimeoutinMicros := int64(util.SecToMicroSec(c.conf.packetTimeout))
	redialTimeoutinMicros := int64(util.SecToMicroSec(c.conf.redialTimeout))
	redialTimeoutSuccessiveRequests := c.conf.packetTimeout * c.conf.sendRate
	var (
		seq              int64 = 1
		timedoutPktCount int
		startTimeout     int64
	)
	for {
		if c.stop {
			c.isStopped.Done()
			return
		}
		for seq < c.packetSequence {
			if c.stop {
				c.isStopped.Done()
				return
			}
			c.mutex.Lock()
			sendTimeStamp, exists := c.pair.PendingRequestsMap[seq]
			if exists {
				now := util.GetTimestampMicroSec()
				if (now - sendTimeStamp) > packetTimeoutinMicros {
					// logrus.Infof("%s-%s: seq: %d, packet timed out: now %d- sendtime %d- timeout %d", c.pair.Source.Name, c.pair.Destination.Name, seq, now, sendTimeStamp, packetTimeoutinMicros)
					c.packetDropped.Inc()
					delete(c.pair.PendingRequestsMap, seq)
					c.mutex.Unlock()
					seq++

					// 1. redial after redial timeout when first event of successive packet drops.
					// 2. redial after redial timeout when dial itself fails.
					// 3. After the succssful first redial attempt, use derived redialTimeoutSuccessiveRequests
					//    to make the further redial attempts.
					// 4. redial only for DN remotes.
					if startTimeout == 0 {
						startTimeout = util.GetTimestampMicroSec()
					} else if startTimeout == -1 {
						timedoutPktCount++
					}
					if c.pair.Destination.IsDN &&
						((startTimeout > 0 && (util.GetTimestampMicroSec()-startTimeout) > redialTimeoutinMicros) ||
							timedoutPktCount > redialTimeoutSuccessiveRequests) {
						err := c.redialDestination()
						if err != nil {
							startTimeout = 0
							logrus.Errorf("error redialling destination %s: %v", c.pair.Destination.Name, err)
						} else {
							startTimeout = -1
						}
						timedoutPktCount = 0
					}
				} else {
					c.mutex.Unlock()
					break
				}
			} else {
				// pkt at seq not a successive timeout stream, reset it.
				if timedoutPktCount > 0 {
					timedoutPktCount = 0
				}
				c.mutex.Unlock()
				seq++
				if seq == c.packetSequence {
					break
				}
			}
		}
		time.Sleep(sleepDuration)
	}
}

// StartPackets start sending packet as per the udp configuration
func (c *udpStream) StartPackets() {
	payLoadSize := c.conf.packetSize - c.msgHeaderLength
	if payLoadSize < 0 {
		payLoadSize = c.msgHeaderLength + 1
		logrus.Errorf("udp packet size %d is too less, recongfigure packet size with %d bytes", c.conf.packetSize, payLoadSize)
	}
	payload, err := util.GetPaddingPayload(payLoadSize)
	if err != nil {
		c.isStopped.Done()
		c.trafficNotStarted.Inc()
		logrus.Errorf("error in getting payload for udp pair %v", *c.pair)
		return
	}
	baseMsg := util.NewMessage(0, 0, c.conf.packetSize)
	baseByteArr, err := msgpack.Marshal(&baseMsg)
	if err != nil {
		c.isStopped.Done()
		c.trafficNotStarted.Inc()
		logrus.Errorf("error in encoding the base udp client message %v", err)
		return
	}
	baseByteArr = append(baseByteArr, payload...)
	interval := util.SecToMicroSec(1) / c.conf.sendRate
	start := util.GetTimestampMicroSec()
	var pausePeriod int64
	for {
		if c.stop {
			c.isStopped.Done()
			return
		}
		if c.conf.suspendTraffic {
			t1 := util.GetTimestampMicroSec()
			time.Sleep(time.Duration(c.conf.packetTimeout) * time.Second)
			pausePeriod += (util.GetTimestampMicroSec() - t1)
			continue
		}

		/* Calculate how many packet to send in this interval */
		targetSeq := ((util.GetTimestampMicroSec() - start - pausePeriod) * int64(c.conf.sendRate)) / 1000000

		/* Send the needed packets */
		for c.packetSequence < targetSeq {
			c.packetSequence++
			sendTimeStamp := util.GetTimestampMicroSec()
			baseMsg.SequenceNumber = c.packetSequence
			baseMsg.SendTimeStamp = sendTimeStamp
			newMsgByteArr, err := msgpack.Marshal(&baseMsg)
			if err != nil {
				c.packetSendFailed.Inc()
				logrus.Errorf("error in encoding the udp client message %v", err)
				continue
			}
			copy(baseByteArr, newMsgByteArr)
			c.mutex.Lock()
			c.pair.PendingRequestsMap[c.packetSequence] = baseMsg.SendTimeStamp
			c.mutex.Unlock()
			_, err = c.connection.Write(baseByteArr)
			if err != nil {
				c.mutex.Lock()
				delete(c.pair.PendingRequestsMap, c.packetSequence)
				c.mutex.Unlock()
				c.packetSendFailed.Inc()
				logrus.Errorf("error in writing message %v to udp client connection: err %v", baseMsg, err)
				continue
			}
			c.packetSent.Inc()
			// logrus.Infof("%s-%s: message sent seq: %d, sendtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, sendTimeStamp)
		}
		/* Sleep for approx. one send interval */
		time.Sleep(util.MicroSecToDuration(interval))
	}
}

func (c *udpStream) redialDestination() error {
	raddr, err := net.ResolveUDPAddr("udp", c.pair.Destination.Name+":"+strconv.Itoa(tapp.UDPServerPort))
	if err != nil {
		return err
	}
	remoteIP := raddr.IP.String()
	if remoteIP == c.pair.Destination.IP {
		return nil
	}
	err = c.connection.Close()
	if err != nil {
		logrus.Warnf("error closing stale udp client connection %s: %v", c.connection.LocalAddr().String(), err)
	}
	logrus.Infof("destination %s changed its ip address to %s, redialling", c.pair.Destination.Name, remoteIP)
	conn, err := net.DialUDP("udp", c.localAddr, raddr)
	if err != nil {
		return err
	}
	// set read buffer size into 512KB
	err = conn.SetReadBuffer(512 * 1024)
	if err != nil {
		return err
	}
	c.connection = conn
	c.pair.Destination.IP = remoteIP
	return nil
}

// TearDownConnection cleans up the udp client connection
func (c *udpStream) TearDownConnection() {
	c.promRegistry.Unregister(c.trafficNotStarted)
	if c.connection == nil {
		return
	}
	c.stop = true
	err := c.connection.Close()
	if err != nil {
		logrus.Warnf("error closing udp client connection %s: %v", c.connection.LocalAddr().String(), err)
	}
	c.isStopped.Wait()
	c.deRegisterStreamMetrics()
	logrus.Infof("udp client connection %s-%s is stopped", c.connection.LocalAddr().String(), c.connection.RemoteAddr().String())
}

type udpClient struct {
	conf *config
}

type config struct {
	sendRate       int
	packetSize     int
	packetTimeout  int
	redialTimeout  int
	suspendTraffic bool
}

// CreateClient create client implementation for the given protocol
func (cm *udpClient) CreateClient(p *util.BatPair, readBufferSize int, reg *prometheus.Registry) (util.ClientImpl, error) {
	return cm.newStream(p, readBufferSize, reg), nil
}

// newStream creates a new udp stream for the given pair
func (cm *udpClient) newStream(p *util.BatPair, readBufferSize int, reg *prometheus.Registry) util.ClientImpl {
	stream := &udpStream{pair: p, mutex: &sync.Mutex{}, stop: false}
	stream.isStopped.Add(3)
	msgHeaderLength, err := util.GetMessageHeaderLength()
	if err != nil {
		panic(err)
	}
	stream.msgHeaderLength = msgHeaderLength
	stream.promRegistry = reg
	stream.streamMetrics = make([]prometheus.Collector, 0)
	stream.readBufferSize = readBufferSize
	stream.conf = cm.conf
	return stream
}

// LoadBatProfileConfig update udp client with the given profile configuration
func (cm *udpClient) LoadBatProfileConfig(profileMap map[string]map[string]string) error {
	if profileMap == nil {
		return errors.New("error parsing the udp profile config map data")
	}
	var err error
	// parse Common profile parameters
	if commonEntry, ok := profileMap["common"]; ok {
		if val, ok := commonEntry["suspend-traffic"]; ok {
			cm.conf.suspendTraffic, err = strconv.ParseBool(val)
			if err != nil {
				return fmt.Errorf("parsing suspend-traffic failed: err %v", err)
			}
		}
	}
	// Parse UDP profile
	if udpEntry, ok := profileMap[tapp.UDPProtocolStr]; ok {
		if val, ok := udpEntry["send-rate"]; ok {
			cm.conf.sendRate, err = cm.parseIntValue(val)
			if err != nil {
				return fmt.Errorf("parsing udp-send-rate failed: err %v", err)
			}
		}

		if val, ok := udpEntry["packet-size"]; ok {
			cm.conf.packetSize, err = cm.parseIntValue(val)
			if err != nil {
				return fmt.Errorf("parsing udp-packet-size failed: err %v", err)
			}
		}

		if val, ok := udpEntry["packet-timeout"]; ok {
			cm.conf.packetTimeout, err = cm.parseIntValue(val)
			if err != nil {
				return fmt.Errorf("parsing udp-packet-timeout failed: err %v", err)
			}
		}

		if val, ok := udpEntry["redial-timeout"]; ok {
			cm.conf.redialTimeout, err = cm.parseIntValue(val)
			if err != nil {
				return fmt.Errorf("parsing udp-redial-period failed: err %v", err)
			}
		}
	}
	logrus.Infof("udp profiling config: %v", *cm.conf)
	return nil
}

func (cm *udpClient) parseIntValue(value string) (int, error) {
	valueInt, err := strconv.Atoi(value)
	if err != nil {
		return -1, err
	}
	return valueInt, nil
}

func init() {
	client := &udpClient{}
	// default config
	client.conf = &config{sendRate: defaultUDPSendRate, packetSize: defaultUDPPacketSize,
		redialTimeout: defaultUDPRedialTimeout, packetTimeout: defaultUDPPacketTimeout,
		suspendTraffic: false}
	tgc.RegisterProtocolClient(tapp.UDPProtocolStr, client)
}
