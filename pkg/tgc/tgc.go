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

package tgc

import (
	"strings"

	"github.com/Nordix/GoBAT/pkg/tapp"
	"github.com/Nordix/GoBAT/pkg/tgen"
	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// podTGC pod traffic controller
type podTGC struct {
	clientSet            kubernetes.Interface
	socketReadBufferSize int
	config               util.Config
	podName              string
	nodeName             string
	namespace            string
	netBatPairs          []util.BatPair
	stopper              chan struct{}
	promRegistry         *prometheus.Registry
	udpServer            util.ServerImpl
}

// TGController traffic gen controller
type TGController interface {
	StartTGC()
	StopTGC()
}

// NewPodTGController creates traffic gen controller for the pod
func NewPodTGController(clientSet kubernetes.Interface, podName, nodeName, namespace string, readBufferSize *int, stopper chan struct{}, reg *prometheus.Registry) TGController {
	return &podTGC{clientSet: clientSet, socketReadBufferSize: *readBufferSize, podName: podName, nodeName: nodeName, namespace: namespace, stopper: stopper, promRegistry: reg}
}

// StartTGC listen for relavant config maps and create pairing
func (tg *podTGC) StartTGC() {
	factory := informers.NewFilteredSharedInformerFactory(tg.clientSet, 0, tg.namespace, func(o *metav1.ListOptions) {
	})
	informer := factory.Core().V1().ConfigMaps().Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cm := obj.(*v1.ConfigMap)
			switch cm.Name {
			case "net-bat-conf":
				batPairs, err := handleAddNetBatConfigMap(cm, tg.podName)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				tg.netBatPairs = batPairs
				if tg.config != nil {
					logrus.Infof("starting tgen clients for pair: %v", tg.netBatPairs)
					tg.createNetBatTgenClients()
				} else {
					logrus.Infof("net bat profile is not configured yet, returning")
				}
				return
			case "storage-bat-conf":
				logrus.Errorf("storage bat config not supported")
				return
			case "dpdk-bat-conf":
				logrus.Errorf("dpdk bat config not supported")
				return
			case "net-bat-profile":
				config, err := util.LoadConfig(cm)
				logrus.Infof("the config values are %v", config)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				tg.config = config
				if config.HasUDPProfile() {
					tappUDPServer, err := tg.startTappServer()
					if err != nil {
						logrus.Errorf("udp server connection creation failed: err %v", err)
						return
					}
					tg.udpServer = tappUDPServer
				}
				if tg.netBatPairs != nil {
					logrus.Infof("net bat profile is set. starting tgen clients for pair: %v", tg.netBatPairs)
					tg.createNetBatTgenClients()
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			cm := newObj.(*v1.ConfigMap)
			switch cm.Name {
			case "net-bat-profile":
				config, err := util.LoadConfig(cm)
				logrus.Infof("the config values are %v", config)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				tg.config = config
				//TODO: should we update the traffic profile for the running traffic ?
			}
		},
		DeleteFunc: func(obj interface{}) {
			cm := obj.(*v1.ConfigMap)
			switch cm.Name {
			case "net-bat-conf":
				tg.deleteNetBatTgenClients()
				return
			case "storage-bat-conf":
				logrus.Errorf("storage bat config not supported")
				return
			case "dpdk-bat-conf":
				logrus.Errorf("dpdk bat config not supported")
				return
			case "net-bat-profile":
				tg.config = nil
				if tg.udpServer != nil {
					tg.udpServer.TearDownServer()
				}
				return
			}
		},
	})
	go informer.Run(tg.stopper)
}

// StopTGC stop listening for config map changes and stop tgc for every pair
func (tg *podTGC) StopTGC() {
	close(tg.stopper)
	tg.deleteNetBatTgenClients()
	if tg.udpServer != nil {
		tg.udpServer.TearDownServer()
	}
}

func (tg *podTGC) createNetBatTgenClients() {
	for index := range tg.netBatPairs {
		go func(p *util.BatPair) {
			p.PendingRequestsMap = make(map[int64]int64)
			p.StartTime = util.GetTimestampMicroSec()
			client, err := tgen.NewClient(p, tg.promRegistry)
			if err != nil {
				logrus.Errorf("error creating client for pair %v: %v", p, err)
				return
			}
			p.ClientConnection = client
			err = p.ClientConnection.SetupConnection(tg.config)
			if err != nil {
				logrus.Errorf("error in setting up the connection for pair %v: %v", p, err)
				return
			}
			go p.ClientConnection.HandleTimeouts(tg.config)
			go p.ClientConnection.SocketRead(tg.socketReadBufferSize)
			go p.ClientConnection.StartPackets(tg.config)
		}((&tg.netBatPairs[index]))
	}
}

func (tg *podTGC) deleteNetBatTgenClients() {
	if tg.netBatPairs == nil {
		logrus.Infof("no bat pairs present to stop")
		return
	}
	for _, pair := range tg.netBatPairs {
		pair.ClientConnection.TearDownConnection()
	}
	tg.netBatPairs = nil
}

func (tg *podTGC) startTappServer() (util.ServerImpl, error) {
	server, err := tapp.NewServer(util.Port, util.ProtocolUDP)
	if err != nil {
		return nil, err
	}
	err = server.SetupServerConnection(tg.config)
	if err != nil {
		return nil, err
	}
	go server.ReadFromSocket(tg.socketReadBufferSize)

	return server, nil
}

func handleAddNetBatConfigMap(cm *v1.ConfigMap, podName string) ([]util.BatPair, error) {
	pairings := []util.BatPair{}
	if val, ok := cm.Data["net-bat-pairing.cfg"]; ok {
		pairings = getAvailableNetBatPairings(podName, val)
	}
	return pairings, nil
}

func getAvailableNetBatPairings(podName, pairingStr string) []util.BatPair {
	lines := strings.Split(string(pairingStr), "\n")
	pairs := make([]util.BatPair, 0)
	for _, line := range lines {
		elements := trimSlice(strings.Split(line, ","))
		// add only pod pairing and ignore others
		if elements[0] != podName {
			continue
		}
		pairs = append(pairs, util.BatPair{SourceIP: elements[1], DestinationIP: elements[2],
			TrafficCase: elements[3], TrafficProfile: elements[4], TrafficType: elements[5]})

	}
	return pairs
}

func trimSlice(strs []string) []string {
	var elements []string
	for _, str := range strs {
		elements = append(elements, strings.TrimSpace(str))
	}
	return elements
}
