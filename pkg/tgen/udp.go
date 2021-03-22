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
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
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
)

// UDPClient udp client implementation
type UDPClient struct {
	isStopped         sync.WaitGroup
	connection        *net.UDPConn
	localAddr         *net.UDPAddr
	pair              *util.BatPair
	packetSequence    int64
	mutex             *sync.Mutex
	msgHeaderLength   int
	stop              bool
	promRegistry      *prometheus.Registry
	metrics           []prometheus.Collector
	trafficNotStarted prometheus.Counter
	packetSent        prometheus.Counter
	packetSendFailed  prometheus.Counter
	packetReceived    prometheus.Counter
	packetDropped     prometheus.Counter
	roundTrip         prometheus.Counter
	latency           prometheus.Summary
}

// NewUDPClient creates a new udp client
func NewUDPClient(p *util.BatPair, reg *prometheus.Registry) util.ClientImpl {
	udpClient := &UDPClient{pair: p, mutex: &sync.Mutex{}, stop: false}
	udpClient.isStopped.Add(3)
	msgHeaderLength, err := util.GetMessageHeaderLength()
	if err != nil {
		panic(err)
	}
	udpClient.msgHeaderLength = msgHeaderLength
	udpClient.promRegistry = reg
	udpClient.metrics = make([]prometheus.Collector, 0)
	return udpClient
}

// SetupConnection sets up udp client connection
func (c *UDPClient) SetupConnection(config util.Config) error {
	labelMap := make(map[string]string)
	labelMap["destination"] = c.pair.Destination.Name
	labelMap["scenario"] = c.pair.TrafficScenario
	labelMap["packet_size"] = strconv.Itoa(config.GetUDPPacketSize())
	labelMap["packet_rate"] = strconv.Itoa(config.GetUDPSendRate())
	source, _ := json.Marshal(c.pair.Source)
	labelMap["source"] = string(source)
	c.trafficNotStarted = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, trafficNotStartedStr, "traffic not started", labelMap)
	c.registerMetric(c.trafficNotStarted)

	var destAddress string
	if util.IsIPv6(c.pair.Destination.Name) {
		destAddress = "[" + c.pair.Destination.Name + "]:" + strconv.Itoa(util.Port)
	} else {
		if !util.IsIPv4(c.pair.Destination.Name) {
			// destination is domain name
			c.pair.Destination.IsDN = true
		}
		destAddress = c.pair.Destination.Name + ":" + strconv.Itoa(util.Port)
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
	logrus.Infof("udp local address: %s, server address: %s connecting ", laddr.String(), c.pair.Destination.IP)
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		c.trafficNotStarted.Inc()
		return err
	}
	// set read buffer size into 512KB
	conn.SetReadBuffer(512 * 1024)
	c.connection = conn
	c.localAddr = laddr

	c.packetSent = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetSentStr, "total packet sent", labelMap)
	c.registerMetric(c.packetSent)

	c.packetSendFailed = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetSendFailedStr, "total packet send failed", labelMap)
	c.registerMetric(c.packetSendFailed)

	c.packetReceived = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetReceivedStr, "total packet received", labelMap)
	c.registerMetric(c.packetReceived)

	c.packetDropped = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, packetDroppedStr, "total packet dropped", labelMap)
	c.registerMetric(c.packetDropped)

	c.roundTrip = util.NewCounter(util.PromNamespace, c.pair.TrafficProfile, roundTripTimeStr, "total round trip time", labelMap)
	c.registerMetric(c.roundTrip)

	objectives := map[float64]float64{0.5: 0.05, 0.9: 0.02, 0.95: 0.01, 0.99: 0.005}
	c.latency = util.NewSummary(util.PromNamespace, c.pair.TrafficProfile, latencyStr, "latency statistics", labelMap, objectives)
	c.registerMetric(c.latency)

	return nil
}

func (c *UDPClient) registerMetric(metric prometheus.Collector) {
	c.promRegistry.MustRegister(metric)
	c.metrics = append(c.metrics, metric)
}

// SocketRead read from udp client socket
func (c *UDPClient) SocketRead(bufSize int) {
	logrus.Infof("udp tgen client read buffer size %d", bufSize)
	receivedByteArr := make([]byte, bufSize)
	for {
		size, _, err := c.connection.ReadFromUDP(receivedByteArr)
		if err != nil {
			logrus.Debugf("error reading message from the udp client connection %v: err %v", c.connection, err)
			if c.stop == true {
				c.isStopped.Done()
				return
			}
			continue
		}
		if size > 0 {
			var msg util.Message
			err := msgpack.Unmarshal(receivedByteArr[:c.msgHeaderLength], &msg)
			if err != nil {
				logrus.Errorf("error in decoding the packet at udp client err %v", err)
				if c.stop == true {
					c.isStopped.Done()
					return
				}
				continue
			}
			//logrus.Infof("%s-%s: message received seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
			c.mutex.Lock()
			_, exists := c.pair.PendingRequestsMap[msg.SequenceNumber]
			if !exists {
				c.mutex.Unlock()
				// msg already timed out
				//logrus.Infof("%s-%s: ignoring message seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
				continue
			}
			//logrus.Infof("%s-%s: processing message seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
			roundTripTime := float64(util.GetTimestampMicroSec() - msg.SendTimeStamp)
			c.roundTrip.Add(roundTripTime)
			c.latency.Observe(roundTripTime)
			c.packetReceived.Inc()
			delete(c.pair.PendingRequestsMap, msg.SequenceNumber)
			c.mutex.Unlock()
		}
		if c.stop == true {
			c.isStopped.Done()
			return
		}
	}
}

// HandleTimeouts handles the message timeouts
func (c *UDPClient) HandleTimeouts(config util.Config) {
	packetTimeout := config.GetUDPPacketTimeout()
	logrus.Infof("udp packet time out: %d", packetTimeout)
	sleepDuration := time.Duration(int64((float64(2.5) / float64(packetTimeout)) * float64(time.Second)))
	packetTimeoutinMicros := int64(util.SecToMicroSec(packetTimeout))
	var seq int64 = 1
	for {
		if c.stop == true {
			c.isStopped.Done()
			return
		}
		for seq < c.packetSequence {
			c.mutex.Lock()
			sendTimeStamp, exists := c.pair.PendingRequestsMap[seq]
			if exists {
				now := util.GetTimestampMicroSec()
				if (now - sendTimeStamp) > packetTimeoutinMicros {
					//logrus.Infof("%s-%s: seq: %d, packet timed out: now %d- sendtime %d- timeout %d", c.pair.Source.Name, c.pair.Destination.Name, seq, now, sendTimeStamp, packetTimeoutinMicros)
					c.packetDropped.Inc()
					delete(c.pair.PendingRequestsMap, seq)
					c.mutex.Unlock()
					seq++
				} else {
					c.mutex.Unlock()
					break
				}
			} else {
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
func (c *UDPClient) StartPackets(config util.Config) {
	packetSize := config.GetUDPPacketSize()
	payLoadSize := packetSize - c.msgHeaderLength
	if payLoadSize < 0 {
		c.trafficNotStarted.Inc()
		logrus.Errorf("udp packet size is too less, recongfigure it with more than %d bytes", c.msgHeaderLength)
		return
	}
	payload, err := util.GetPaddingPayload(payLoadSize)
	if err != nil {
		c.trafficNotStarted.Inc()
		logrus.Errorf("error in getting payload for udp pair %v", *c.pair)
		return
	}
	baseMsg := util.NewMessage(0, 0, packetSize)
	baseByteArr, err := msgpack.Marshal(&baseMsg)
	if err != nil {
		c.trafficNotStarted.Inc()
		logrus.Errorf("error in encoding the base udp client message %v", err)
		return
	}
	baseByteArr = append(baseByteArr, payload...)
	sendRate := config.GetUDPSendRate()
	interval := util.SecToMicroSec(1) / sendRate
	start := util.GetTimestampMicroSec()
	redialPeriodInMicros := int64(util.SecToMicroSec(config.GetUDPRedialPeriod()))
	nextRedial := start + redialPeriodInMicros
	for {
		if config.SuspendTraffic() {
			time.Sleep(time.Duration(config.GetUDPPacketTimeout()) * time.Second)
			continue
		}
		currentTimeStamp := util.GetTimestampMicroSec()
		if c.pair.Destination.IsDN && currentTimeStamp > nextRedial {
			err := c.redialDestination(config)
			if err != nil {
				c.trafficNotStarted.Inc()
				logrus.Errorf("error redialling destination %s: %v", c.pair.Destination.Name, err)
				if c.stop == true {
					c.isStopped.Done()
					return
				}
				time.Sleep(time.Duration(config.GetUDPPacketTimeout()) * time.Second)
				continue
			} else {
				nextRedial = currentTimeStamp + redialPeriodInMicros
			}
		}
		/* Calculate how many packet to send in this interval */
		targetSeq := ((currentTimeStamp - start) * int64(sendRate)) / 1000000

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
				if c.stop == true {
					c.isStopped.Done()
					return
				}
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
				if c.stop == true {
					c.isStopped.Done()
					return
				}
				continue
			}
			c.packetSent.Inc()
			//logrus.Infof("%s-%s: message sent seq: %d, sendtimestamp: %d", c.pair.Source.Name, c.pair.Destination.Name, c.packetSequence, sendTimeStamp)
		}
		/* Sleep for approx. one send interval */
		time.Sleep(util.MicroSecToDuration(interval))

		if c.stop == true {
			c.isStopped.Done()
			return
		}
	}
}

func (c *UDPClient) redialDestination(config util.Config) error {
	raddr, err := net.ResolveUDPAddr("udp", c.pair.Destination.Name+":"+strconv.Itoa(util.Port))
	if err != nil {
		return err
	}
	remoteIP := raddr.IP.String()
	if remoteIP == c.pair.Destination.IP {
		return nil
	}
	c.connection.Close()
	logrus.Infof("destination %s changed its ip address to %s, redialling", c.pair.Destination.Name, remoteIP)
	conn, err := net.DialUDP("udp", c.localAddr, raddr)
	if err != nil {
		return err
	}
	// set read buffer size into 512KB
	conn.SetReadBuffer(512 * 1024)
	c.connection = conn
	c.pair.Destination.IP = remoteIP
	return nil
}

// TearDownConnection cleans up the udp client connection
func (c *UDPClient) TearDownConnection() {
	if c.connection == nil {
		c.promRegistry.Unregister(c.trafficNotStarted)
		return
	}
	c.stop = true
	c.connection.Close()
	c.isStopped.Wait()
	for _, metric := range c.metrics {
		c.promRegistry.Unregister(metric)
	}
	logrus.Infof("udp client connection %s-%s is stopped", c.connection.LocalAddr().String(), c.connection.RemoteAddr().String())
}
