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
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bufio"
	"os"
	"path/filepath"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"

	"github.com/golang/glog"
	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/openshift/app-netutil/pkg/networkstatus"
	"github.com/openshift/app-netutil/pkg/types"
	userplugin "github.com/openshift/app-netutil/pkg/userspace"
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

type Remote struct {
	IsDN bool
	Name string
	IP   string
}

// BatPair represents the BAT traffic to be run between two entities
type BatPair struct {
	Source             *Source
	Destination        *Remote
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
	ServerNameLength int
	Length           int
}

// Server struct used by protocol server
type Server struct {
	HostName  string
	IPAddress string
	Port      int
}

// ServerImpl methods to be implemented by a server
type ServerImpl interface {
	SetupServerConnection(Config) error
	ReadFromSocket(bufSize int)
	HandleIdleConnections(Config)
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
func NewMessage(sequence, sendTimeStamp int64, packetSize int) *Message {
	return &Message{SequenceNumber: sequence, SendTimeStamp: sendTimeStamp, RespondTimeStamp: 0, ServerNameLength: 0, Length: packetSize}
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
	msg := Message{SequenceNumber: 0, SendTimeStamp: 0, RespondTimeStamp: 0, Length: 0}
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

// NewCounter API to create a new counter
func NewCounter(namespace, subsystem, name, help string, labelMap map[string]string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   namespace,
		Subsystem:   subsystem,
		Name:        name,
		Help:        help,
		ConstLabels: labelMap,
	})
}

// NewGauge API to create a new gauge metric
func NewGauge(namespace, subsystem, name, help string, labelMap map[string]string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Subsystem:   subsystem,
		Name:        name,
		Help:        help,
		ConstLabels: labelMap,
	})
}

// NewSummary API to create a new summary metric
func NewSummary(namespace, subsystem, name, help string, labelMap map[string]string, objectives map[float64]float64) prometheus.Summary {
	return prometheus.NewSummary(prometheus.SummaryOpts{
		Namespace:   namespace,
		Subsystem:   subsystem,
		Name:        name,
		Help:        help,
		ConstLabels: labelMap,
		Objectives:  objectives,
	},
	)
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
	return ip != nil && strings.Count(ipAddress, ":") >= 2
}

// IsIPv4 to check ipAddress is either ipv4 or not
func IsIPv4(ipAddress string) bool {
	ip := net.ParseIP(ipAddress)
	return ip != nil && strings.Count(ipAddress, ":") < 2
}

// GetNetInterfaces return interfaces map[name]ipAddress
func GetNetInterfaces() (map[string]string, error) {
	ifaceMap := make(map[string]string)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	if len(ifaces) == 0 {
		return nil, errors.New("no interfaces present")
	}
	for _, iface := range ifaces {
		logrus.Infof("found interface %s", iface.Name)
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
		logrus.Infof("interface %s has ip address %s", iface.Name, ipNet.IP.String())
		ifaceMap[iface.Name] = ipNet.IP.String()
	}
	return ifaceMap, nil
}

// The following methods are copied from https://github.com/openshift/app-netutil/ project
// SPDX-License-Identifier: Apache-2.0
// Copyright(c) 2019 Red Hat, Inc.

//
// This module reads and parses any configuration data provided
// to a container by the host. This module manages the
// file operations and the mapping between the data format
// of the provided configuration data and the data format used
// by app-netutil.
//
// Currently, configuration data can be passed to the container
// thru Environmental Variables, Annotations, or shared files.
//

//
// Types
//

//
// API Functions
//
func GetInterfaces() (*types.InterfaceResponse, error) {
	glog.Infof("GetInterfaces: ENTER")

	response := &types.InterfaceResponse{}

	// Open Annotations File
	annotationPath := filepath.Join("/etc/podinfo", "annotations")
	if _, err := os.Stat(annotationPath); err != nil {
		if os.IsNotExist(err) {
			glog.Infof("GetInterfaces: \"annotations\" file: %v does not exist.", annotationPath)
		}
	} else {
		file, err := os.Open(annotationPath)
		if err != nil {
			glog.Errorf("GetInterfaces: Error opening \"annotations\" file: %v ", err)
			return response, err
		}
		defer file.Close()

		// Buffers to store unmarshalled data (from annotations
		// or files) used by app-netutil
		netStatData := &networkstatus.NetStatusData{}
		usrspData := &userplugin.UserspacePlugin{}

		//
		// Parse the file into individual annotations
		//
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			status := strings.Split(string(line), "\n")

			// Loop through each annotation
			for _, s := range status {
				glog.Infof("  s-%v", s)
				parts := strings.Split(string(s), "=")

				// DEBUG
				glog.Infof("  PartsLen-%d", len(parts))
				if len(parts) >= 1 {
					glog.Infof("  parts[0]-%s", parts[0])
				}

				if len(parts) == 2 {

					// Remove the Indent from the original marshalling
					parts[1] = strings.Replace(string(parts[1]), "\\n", "", -1)
					parts[1] = strings.Replace(string(parts[1]), "\\", "", -1)
					parts[1] = strings.Replace(string(parts[1]), " ", "", -1)
					parts[1] = string(parts[1][1 : len(parts[1])-1])

					// Parse any NetworkStatus Annotations. Values will be
					// saved in netStatData structure for later.
					networkstatus.ParseAnnotations(parts[0], parts[1], netStatData)

					// Parse any Userspace Annotations. Values will be
					// saved in usrspData structure for later.
					userplugin.ParseAnnotations(parts[0], parts[1], usrspData)
				}
			}
		}
		// Append any NetworkStatus collected data to the list
		// of interfaces.
		//
		// Because return data is based on NetworkStatus, call NetworkStatus
		// processing first. For efficiency, it assumes no interfaces have been
		// added to list, so it doesn't search existing list to make sure a given
		// interfaces has not already been added.
		networkstatus.AppendInterfaceData(netStatData, response)

		// Append any Userspace collected data to the list
		// of interfaces.
		userplugin.AppendInterfaceData(usrspData, response)
	}

	// PCI Address for SR-IOV Interfaces are found in
	// Environmental Variables. Search through them to
	// see if any can be found.
	glog.Infof("PROCESS ENV:")
	envResponse, err := getEnv()
	if err != nil {
		glog.Errorf("GetInterfaces: Error calling getEnv: %v", err)
		return nil, err
	}
	pciAddressSlice := []string{}
	for k, v := range envResponse.Envs {
		if strings.HasPrefix(k, "PCIDEVICE") {
			glog.Infof("  k:%v v:%v", k, v)
			valueParts := strings.Split(string(v), ",")
			for _, id := range valueParts {
				found := false
				// DeviceInfo in the NetworkStatus annotation also has PCI Address.
				// So skip if PCI Address already found.
				for _, ifaceData := range response.Interface {
					if ifaceData.NetworkStatus.DeviceInfo != nil {
						if ifaceData.NetworkStatus.DeviceInfo.Pci != nil &&
							strings.EqualFold(ifaceData.NetworkStatus.DeviceInfo.Pci.PciAddress, id) {
							// PCI Address in ENV matched that in DeviceInfo. Mark as SR-IOV.
							ifaceData.DeviceType = types.INTERFACE_TYPE_SRIOV
							found = true
							break
						}
						if ifaceData.NetworkStatus.DeviceInfo.Vdpa != nil &&
							strings.EqualFold(ifaceData.NetworkStatus.DeviceInfo.Vdpa.PciAddress, id) {
							// PCI Address in ENV matched that in DeviceInfo.
							// Leave the vDPA device and skip processing of the SR-IOV Interface
							found = true
							break
						}
					}
				}
				if found {
					glog.Infof("     Skip Adding ID:%v", id)
				} else {
					glog.Infof("     Adding ID:%v", id)
					pciAddressSlice = append(pciAddressSlice, id)
				}
			}
		}
	}

	// Determine how many detected interfaces with type "unknown"
	var unknownCnt int
	for _, ifaceData := range response.Interface {
		if ifaceData.DeviceType == types.INTERFACE_TYPE_UNKNOWN {
			unknownCnt++
		}
	}

	var pciIndex int
	for _, ifaceData := range response.Interface {
		if ifaceData.DeviceType == types.INTERFACE_TYPE_UNKNOWN {
			// If there are more "unknown" interface types than there are
			// PCI interfaces not in the list, then mark the "default"
			// interface as a host interface.
			if ifaceData.NetworkStatus.Default && unknownCnt > len(pciAddressSlice) {
				ifaceData.DeviceType = types.INTERFACE_TYPE_HOST
				unknownCnt--
				glog.Infof("%s is the \"default\" interface, mark as \"%s\"",
					ifaceData.NetworkStatus.Interface, ifaceData.DeviceType)
			} else if pciIndex < len(pciAddressSlice) {
				// Since type was "unknown" and there are PCI interfaces not yet
				// in the list, add the PCI interfaces one by one.
				if ifaceData.NetworkStatus.DeviceInfo == nil {
					ifaceData.DeviceType = types.INTERFACE_TYPE_SRIOV
					unknownCnt--
					ifaceData.NetworkStatus.DeviceInfo = &nettypes.DeviceInfo{
						Type:    nettypes.DeviceInfoTypePCI,
						Version: nettypes.DeviceInfoVersion,
						Pci: &nettypes.PciDevice{
							PciAddress: pciAddressSlice[pciIndex],
						},
					}
					pciIndex++
					glog.Infof("%s was \"unknown\", mark as \"%s\"",
						ifaceData.NetworkStatus.Interface, ifaceData.DeviceType)
				} else {
					glog.Warningf("%s was \"unknown\", but DeviceInfo exists with type \"%s\"",
						ifaceData.NetworkStatus.Interface, ifaceData.NetworkStatus.DeviceInfo.Type)
				}
			} else {
				// Since there are no more PCI interfaces not in the list, and the
				// type is unknown, mark this interface as "host".
				ifaceData.DeviceType = types.INTERFACE_TYPE_HOST
				unknownCnt--
				glog.Infof("%s was \"unknown\", mark as \"%s\"",
					ifaceData.NetworkStatus.Interface, ifaceData.DeviceType)
			}
		}
	}

	// PCI Address found that did not match an existing interface in the
	// NetworkStatus annotation so add to list.
	if pciIndex < len(pciAddressSlice) {
		for _, pciAddr := range pciAddressSlice[pciIndex:] {
			ifaceData := &types.InterfaceData{
				DeviceType: types.INTERFACE_TYPE_SRIOV,
				NetworkStatus: nettypes.NetworkStatus{
					DeviceInfo: &nettypes.DeviceInfo{
						Type:    nettypes.DeviceInfoTypePCI,
						Version: nettypes.DeviceInfoVersion,
						Pci: &nettypes.PciDevice{
							PciAddress: pciAddr,
						},
					},
				},
			}
			response.Interface = append(response.Interface, ifaceData)

			glog.Infof("Adding %s as new interface because no other matches, type \"%s\"",
				pciAddr, ifaceData.DeviceType)
		}
	}

	glog.Infof("RESPONSE:")
	for _, ifaceData := range response.Interface {
		glog.Infof("%v", ifaceData)
	}

	return response, err
}

type EnvResponse struct {
	Envs map[string]string
}

func getEnv() (*EnvResponse, error) {
	path := filepath.Join("/proc", strconv.Itoa(os.Getpid()), "environ")
	glog.Infof("getting environment variables from path: %s", path)
	file, err := os.Open(path)
	if err != nil {
		glog.Errorf("Error openning proc environ file: %v", err)
		return nil, err
	}
	defer file.Close()

	envAttrs := make(map[string]string)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		envs := strings.Split(string(line), "\x00")
		for _, e := range envs {
			parts := strings.Split(string(e), "=")
			if len(parts) == 2 {
				envAttrs[parts[0]] = parts[1]
			}
		}
	}
	return &EnvResponse{Envs: envAttrs}, nil
}
