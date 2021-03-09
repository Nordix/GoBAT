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
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/Nordix/GoBAT/pkg/tapp"
	"github.com/Nordix/GoBAT/pkg/tgen"
	"github.com/Nordix/GoBAT/pkg/util"
	netlib "github.com/openshift/app-netutil/lib/v1alpha"
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
	serversMap           map[string][]util.ServerImpl
	mutex                *sync.Mutex
}

// TGController traffic gen controller
type TGController interface {
	StartTGC()
	StopTGC()
}

// NewPodTGController creates traffic gen controller for the pod
func NewPodTGController(clientSet kubernetes.Interface, podName, nodeName, namespace string,
	readBufferSize *int, stopper chan struct{}, reg *prometheus.Registry) TGController {
	return &podTGC{clientSet: clientSet, socketReadBufferSize: *readBufferSize,
		podName: podName, nodeName: nodeName, namespace: namespace,
		stopper: stopper, promRegistry: reg, mutex: &sync.Mutex{},
		serversMap: make(map[string][]util.ServerImpl)}
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
			case "net-bat-pairing":
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				tg.handleNetBatPairingAddEvent(cm)
				return
			case "storage-bat-conf":
				logrus.Errorf("storage bat config not supported")
				return
			case "dpdk-bat-conf":
				logrus.Errorf("dpdk bat config not supported")
				return
			case "net-bat-profile":
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				config, err := util.LoadConfig(cm)
				logrus.Infof("traffic profile: %v", config)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				tg.config = config
				for _, protocol := range config.GetProfiles() {
					servers, err := tapp.NewServer(tg.socketReadBufferSize, util.Port, protocol, tg.config)
					if err != nil {
						logrus.Errorf("server for %s, creation failed: err %v", protocol, err)
						continue
					}
					tg.serversMap[protocol] = servers
				}
				if tg.netBatPairs != nil {
					logrus.Infof("net bat profile is set. starting tgen clients")
					for _, batPair := range tg.netBatPairs {
						logrus.Infof("bat pair %v, %s, %s, %s", *batPair.Source, batPair.Destination, batPair.TrafficProfile, batPair.TrafficScenario)
					}
					tg.createNetBatTgenClients()
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			cm := newObj.(*v1.ConfigMap)
			switch cm.Name {
			case "net-bat-pairing":
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				// reconfigure the clients with new pairing config
				tg.deleteNetBatTgenClients()
				tg.handleNetBatPairingAddEvent(cm)
				return
			case "net-bat-profile":
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				config, err := util.LoadConfig(cm)
				logrus.Infof("updated traffic profile: %v", config)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				tg.config = config
				// restart the client with new config settings
				tg.restartNetBatTgenClients()
			}
		},
		DeleteFunc: func(obj interface{}) {
			cm := obj.(*v1.ConfigMap)
			switch cm.Name {
			case "net-bat-pairing":
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				tg.deleteNetBatTgenClients()
				return
			case "storage-bat-conf":
				logrus.Errorf("storage bat config not supported")
				return
			case "dpdk-bat-conf":
				logrus.Errorf("dpdk bat config not supported")
				return
			case "net-bat-profile":
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				tg.stopServers()
				tg.config = nil
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
	tg.stopServers()
}

func (tg *podTGC) stopServers() {
	for _, servers := range tg.serversMap {
		for _, server := range servers {
			server.TearDownServer()
		}
	}
}

func (tg *podTGC) handleNetBatPairingAddEvent(cm *v1.ConfigMap) {
	batPairs, err := handleAddNetBatConfigMap(cm, tg.namespace, tg.podName)
	if err != nil {
		logrus.Errorf("error parsing the net bat config map: %v", err)
		return
	}
	tg.netBatPairs = batPairs
	if tg.config != nil {
		for _, batPair := range tg.netBatPairs {
			logrus.Infof("bat pair %v, %s, %s, %s", *batPair.Source, batPair.Destination, batPair.TrafficProfile, batPair.TrafficScenario)
		}
		tg.createNetBatTgenClients()
	} else {
		logrus.Infof("net bat profile is not configured yet, returning")
	}
}

func (tg *podTGC) restartNetBatTgenClients() {
	if tg.netBatPairs == nil {
		return
	}
	for _, pair := range tg.netBatPairs {
		pair.ClientConnection.TearDownConnection()
	}
	tg.createNetBatTgenClients()
}

func (tg *podTGC) createNetBatTgenClients() {
	for index := range tg.netBatPairs {
		go func(p *util.BatPair) {
			p.PendingRequestsMap = make(map[int64]int64)
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

func handleAddNetBatConfigMap(cm *v1.ConfigMap, namespace, podName string) ([]util.BatPair, error) {
	pairings := []util.BatPair{}
	var err error
	if val, ok := cm.Data["net-bat-pairing.cfg"]; ok {
		pairings, err = getAvailableNetBatPairings(namespace, podName, val)
		if err != nil {
			return nil, err
		}
	}
	return pairings, nil
}

func getAvailableNetBatPairings(namespace, podName, pairingStr string) ([]util.BatPair, error) {
	lines := strings.Split(string(pairingStr), "\n")
	pairs := make([]util.BatPair, 0)
	ifaceResponse, err := netlib.GetInterfaces()
	if err != nil {
		return nil, err
	}
	pairMap := make(map[string][]util.BatPair)
	rand.Seed(time.Now().Unix())
	for _, line := range lines {
		pair := strings.TrimSpace(line)
		if pair == "" || strings.HasPrefix(pair, "#") {
			continue
		}
		elements := strings.Split(pair, "},")
		source := &util.Source{}
		sourceStr := strings.TrimSpace(elements[0] + "}")
		if err := json.Unmarshal([]byte(sourceStr), source); err != nil {
			logrus.Errorf("error in parsing the source for pair %v: %v", sourceStr, err)
			continue
		}
		pairType := source.Type
		pairName := source.Name
		isPod := strings.EqualFold(pairType, "pod")
		isDeployment := strings.EqualFold(pairType, "deployment")
		isDaemonSet := strings.EqualFold(pairType, "daemonset")
		if source.Namespace != namespace {
			continue
		} else if !isPod && !isDeployment && !isDaemonSet {
			logrus.Errorf("unsupported source type in the pair %s, ignoring", pair)
			continue
		} else if isPod && pairName != podName {
			continue
		} else if (isDeployment || isDaemonSet) && !strings.HasPrefix(podName, pairName) {
			continue
		}
		var primaryIfaceIPAddress string
		for _, iface := range ifaceResponse.Interface {
			if source.Net != "" && iface.NetworkStatus.Name == namespace+"/"+source.Net {
				source.IP = iface.NetworkStatus.IPs[0]
				break
			} else if source.Interface != "" && iface.NetworkStatus.Interface == source.Interface {
				source.IP = iface.NetworkStatus.IPs[0]
				break
			}
			if iface.NetworkStatus.Interface == "" {
				primaryIfaceIPAddress = iface.NetworkStatus.IPs[0]
			}
		}
		if source.IP == "" {
			// assign primary network eth0 interface ip address
			source.IP = primaryIfaceIPAddress
		}
		elements = trimSlice(strings.Split(elements[1], ","))
		batPair := util.BatPair{Source: source, Destination: elements[0], TrafficProfile: elements[1], TrafficScenario: elements[2]}
		if batPairs, ok := pairMap[sourceStr]; ok {
			pairMap[sourceStr] = append(batPairs, batPair)
		} else {
			pairMap[sourceStr] = []util.BatPair{batPair}
		}
	}
	for _, tempPairs := range pairMap {
		if len(tempPairs) == 1 {
			pairs = append(pairs, tempPairs[0])
		} else {
			// pick up a random destination for the pair
			pairs = append(pairs, tempPairs[rand.Intn(len(tempPairs))])
		}
	}
	return pairs, nil
}

func trimSlice(strs []string) []string {
	var elements []string
	for _, str := range strs {
		elements = append(elements, strings.TrimSpace(str))
	}
	return elements
}
