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

package util

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/vmihailenco/msgpack"
)

const (
	// ProtocolHTTP http protocol string
	ProtocolHTTP = "http"
	// ProtocolUDP udp protocol string
	ProtocolUDP = "udp"
	//PromNamespace prometheus udp namespace string
	PromNamespace = "netbat"
	// MaxBufferSize max buffer size
	MaxBufferSize = 65535
	// Port server port
	Port = 8890
)

// Source represents source of the BatPair
type Source struct {
	Type      string `json:"type"`
	Namespace string `json:"ns"`
	Name      string `json:"name"`
	Net       string `json:"net,omitempty"`
	Interface string `json:"interface,omitempty"`
	IP        string `json:"ip,omitempty"`
}

// Error error associated with pair
type Error struct {
	Code        int
	Description string
}

// BatPair represents the BAT traffic to be run between two entities
type BatPair struct {
	Source             *Source
	Destination        string
	TrafficProfile     string
	TrafficScenario    string
	PendingRequestsMap map[int64]int64
	ClientConnection   ClientImpl
}

// Message to be sent and received by protocol clients
type Message struct {
	SequenceNumber   int64
	SendTimeStamp    int64
	RespondTimeStamp int64
}

// Server struct used by protocol server
type Server struct {
	IPAddress string
	Port      int
}

// ServerImpl methods to be implemented by a server
type ServerImpl interface {
	SetupServerConnection(Config) error
	ReadFromSocket(bufSize int)
	TearDownServer()
}

// ClientImpl methods to be implemented by a client
type ClientImpl interface {
	SetupConnection(Config) error
	TearDownConnection()
	SocketRead(bufSize int)
	HandleTimeouts(Config)
	StartPackets(Config)
}

// NewMessage creates a new message
func NewMessage(sequence, sendTimeStamp int64) *Message {
	return &Message{SequenceNumber: sequence, SendTimeStamp: sendTimeStamp, RespondTimeStamp: 0}
}

// GetPaddingPayload get payload for the given length
func GetPaddingPayload(payloadSize int) ([]byte, error) {
	payload := make([]byte, payloadSize)
	for index := range payload {
		payload[index] = 0xff
	}
	return payload, nil
}

// GetMessageHeaderLength get message header length
func GetMessageHeaderLength() (int, error) {
	msg := Message{SequenceNumber: 0, SendTimeStamp: 0, RespondTimeStamp: 0}
	byteArr, err := msgpack.Marshal(msg)
	if err != nil {
		return -1, err
	}
	return len(byteArr), nil
}

// GetTimestampMicroSec get timestamp in microseconds
func GetTimestampMicroSec() int64 {
	return time.Now().UnixNano() / int64(time.Microsecond)
}

// SecToMicroSec convert given seconds into microseconds
func SecToMicroSec(sec int) int {
	return (sec * int(time.Second) / int(time.Microsecond))
}

// MicroSecToDuration convert give msec into duration
func MicroSecToDuration(msec int) time.Duration {
	return time.Duration(msec) * time.Microsecond
}

// NewCounter creates a new counter and registers with prometheus
func NewCounter(namespace, subsystem, name, help string, labelMap map[string]string) prometheus.Counter {
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   namespace,
		Subsystem:   subsystem,
		Name:        name,
		Help:        help,
		ConstLabels: labelMap,
	})
	return counter
}

// RegisterPromHandler register prometheus http handler
func RegisterPromHandler(promPort int, reg *prometheus.Registry) {
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	http.Handle("/metrics", handler)
	http.ListenAndServe(":"+strconv.Itoa(promPort), nil)
}

// IsIPv6 to check ipAddress is either ipv6 or not
func IsIPv6(ipAddress string) bool {
	ip := net.ParseIP(ipAddress)
	return ip != nil && strings.Contains(ipAddress, ":")
}
