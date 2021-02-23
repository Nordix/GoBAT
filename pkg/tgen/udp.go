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
	// packetSent represents total packets sent
	packetSentStr = "packets_sent"
	// packetReceived represents total packets received
	packetReceivedStr = "packets_received"
	// packetDropped represents total packets received
	packetDroppedStr = "packets_dropped"
	// roundTripTime represent total round trip time
	roundTripTimeStr = "total_round_trip_time"
)

// UDPClient udp client implementation
type UDPClient struct {
	isStopped       sync.WaitGroup
	connection      *net.UDPConn
	pair            *util.BatPair
	packetSequence  int64
	mutex           *sync.Mutex
	msgHeaderLength int
	stop            bool
	promRegistry    *prometheus.Registry
	packetSent      prometheus.Counter
	packetReceived  prometheus.Counter
	packetDropped   prometheus.Counter
	roundTrip       prometheus.Counter
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
	return udpClient
}

// SetupConnection sets up udp client connection
func (c *UDPClient) SetupConnection() error {
	raddr, err := net.ResolveUDPAddr("udp", c.pair.DestinationIP+":"+strconv.Itoa(util.Port))
	if err != nil {
		return err
	}
	laddr, err := net.ResolveUDPAddr("udp", c.pair.SourceIP+":0")
	logrus.Infof("local address: %s, server address: %s connecting ", laddr.String(), raddr.String())
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return err
	}
	c.connection = conn
	labelMap := make(map[string]string)
	labelMap["source"] = c.pair.SourceIP
	labelMap["destination"] = c.pair.DestinationIP
	c.packetSent = util.NewCounter(c.pair.TrafficType, c.pair.TrafficCase, packetSentStr, "total packet sent", labelMap)
	c.promRegistry.MustRegister(c.packetSent)
	c.packetReceived = util.NewCounter(c.pair.TrafficType, c.pair.TrafficCase, packetReceivedStr, "total packet received", labelMap)
	c.promRegistry.MustRegister(c.packetReceived)
	c.packetDropped = util.NewCounter(c.pair.TrafficType, c.pair.TrafficCase, packetDroppedStr, "total packet dropped", labelMap)
	c.promRegistry.MustRegister(c.packetDropped)
	c.roundTrip = util.NewCounter(c.pair.TrafficType, c.pair.TrafficCase, roundTripTimeStr, "total round trip time", labelMap)
	c.promRegistry.MustRegister(c.roundTrip)
	return nil
}

// SocketRead read from udp client socket
func (c *UDPClient) SocketRead(bufSize int) {
	logrus.Infof("tgen client read buffer size %d", bufSize)
	receivedByteArr := make([]byte, bufSize)
	for {
		size, _, err := c.connection.ReadFromUDP(receivedByteArr)
		if err != nil {
			logrus.Errorf("error reading message from the client connection %v: err %v", c.connection, err)
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
				logrus.Errorf("error in decoding the packet at client err %v", err)
				if c.stop == true {
					c.isStopped.Done()
					return
				}
				continue
			}
			//logrus.Infof("%s-%s: message received seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.SourceIP, c.pair.DestinationIP, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
			c.mutex.Lock()
			_, exists := c.pair.PendingRequestsMap[msg.SequenceNumber]
			if !exists {
				c.mutex.Unlock()
				// msg already timed out
				//logrus.Infof("%s-%s: ignoring message seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.SourceIP, c.pair.DestinationIP, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
				continue
			}
			//logrus.Infof("%s-%s: processing message seq: %d, sendtimestamp: %d, respondtimestamp: %d", c.pair.SourceIP, c.pair.DestinationIP, c.packetSequence, msg.SendTimeStamp, msg.RespondTimeStamp)
			c.roundTrip.Add(float64(util.GetTimestampMicroSec() - msg.SendTimeStamp))
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
	sleepDuration := time.Duration(int64((float64(2.5) / float64(config.GetUDPPacketTimeout())) * float64(time.Second)))
	//logrus.Infof("udp packet time out: %d", config.GetUDPPacketTimeout())
	//logrus.Infof("tick at %s", time.Duration(int64((float64(1)/float64(config.GetUDPPacketTimeout()))*float64(time.Second))))
	packetTimeoutinMicros := int64(util.SecToMicroSec(config.GetUDPPacketTimeout()))
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
					//logrus.Infof("%s-%s: seq: %d, packet timed out: now %d- sendtime %d- timeout %d", c.pair.SourceIP, c.pair.DestinationIP, seq, now, sendTimeStamp, packetTimeoutinMicros)
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
	payload, err := util.GetPaddingPayload(packetSize - c.msgHeaderLength)
	if err != nil {
		logrus.Errorf("error in getting payload for pair %v", *c.pair)
		return
	}
	baseMsg := util.NewMessage(packetSize, 0, 0)
	baseByteArr, err := msgpack.Marshal(&baseMsg)
	if err != nil {
		logrus.Errorf("error in encoding the base client message %v", err)
		return
	}
	baseByteArr = append(baseByteArr, payload...)
	sendRate := config.GetUDPSendRate()
	interval := util.SecToMicroSec(1) / sendRate
	start := util.GetTimestampMicroSec()
	for {
		/* Calculate how many packet to send in this interval */
		targetSeq := ((util.GetTimestampMicroSec() - start) * int64(sendRate)) / 1000000

		/* Send the needed packets */
		for c.packetSequence < targetSeq {
			c.packetSequence++
			sendTimeStamp := util.GetTimestampMicroSec()
			baseMsg.SequenceNumber = c.packetSequence
			baseMsg.SendTimeStamp = sendTimeStamp
			newMsgByteArr, err := msgpack.Marshal(&baseMsg)
			if err != nil {
				logrus.Errorf("error in encoding the client message %v", err)
				if c.stop == true {
					c.isStopped.Done()
					return
				}
				continue
			}
			copy(baseByteArr, newMsgByteArr)
			_, err = c.connection.Write(baseByteArr)
			if err != nil {
				logrus.Errorf("error in writing message %v to client connection: err %v", baseMsg, err)
				if c.stop == true {
					c.isStopped.Done()
					return
				}
				continue
			}
			c.packetSent.Inc()
			c.mutex.Lock()
			c.pair.PendingRequestsMap[c.packetSequence] = baseMsg.SendTimeStamp
			c.mutex.Unlock()
			//logrus.Infof("%s-%s: message sent seq: %d, sendtimestamp: %d", c.pair.SourceIP, c.pair.DestinationIP, c.packetSequence, sendTimeStamp)
		}
		/* Sleep for approx. one send interval */
		time.Sleep(util.MicroSecToDuration(interval))

		if c.stop == true {
			c.isStopped.Done()
			return
		}
	}
}

// TearDownConnection cleans up the udp client connection
func (c *UDPClient) TearDownConnection() {
	c.stop = true
	c.connection.Close()
	c.isStopped.Wait()
	c.promRegistry.Unregister(c.packetSent)
	c.promRegistry.Unregister(c.packetReceived)
	c.promRegistry.Unregister(c.packetDropped)
	c.promRegistry.Unregister(c.roundTrip)
	logrus.Infof("client connection %s-%s is stopped", c.connection.LocalAddr().String(), c.connection.RemoteAddr().String())
}
