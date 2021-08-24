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

// Package tgc contains controller implementation which reacts to config map events,
// registration, start and teardown of tgen, tapp instances
package tgc

import (
	"bytes"
	"compress/gzip"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/Nordix/GoBAT/pkg/util"
)

var (
	protoServers = make(map[string]*util.ProtocolServerModule)
	protoClients = make(map[string]*util.ProtocolClientModule)
)

const (
	netBatPairing = "net-bat-pairing"
	netBatProfile = "net-bat-profile"
)

// podTGC pod traffic controller
type podTGC struct {
	clientSet              kubernetes.Interface
	socketReadBufferSize   int
	config                 util.Config
	podName                string
	nodeName               string
	namespace              string
	netBatPairs            []util.BatPair
	netPairResourceVersion string
	stopper                chan struct{}
	isRunning              int32
	promRegistry           *prometheus.Registry
	serversMap             map[string][]util.ServerImpl
	mutex                  *sync.Mutex
}

// TGController traffic gen controller
type TGController interface {
	StartTGC()
	StopTGC()
}

// RegisterProtocolServer registers the given protocol server with given key
func RegisterProtocolServer(protocol string, server util.ProtocolServerModule) {
	protoServers[protocol] = &server
}

// RegisterProtocolClient registers the given protocol client with given key
func RegisterProtocolClient(protocol string, client util.ProtocolClientModule) {
	protoClients[protocol] = &client
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
			case netBatPairing:
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				tg.netPairResourceVersion = cm.GetResourceVersion()
				tg.handleNetBatPairingAddEvent(cm)
				return
			case netBatProfile:
				tg.mutex.Lock()
				defer tg.mutex.Unlock()

				config, err := util.LoadConfig(cm)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				tg.config = config
				ifNameAddressMap, err := util.GetNetInterfaces()
				if err != nil {
					logrus.Errorf("error retrieving pod interfaces: error %v", err)
					return
				}
				for protocol, sm := range protoServers {
					logrus.Infof("available bat server: %s", protocol)
					err = (*sm).LoadBatProfileConfig(tg.config.GetProfileMap())
					if err != nil {
						logrus.Errorf("error creating %s server profile config: error %v", protocol, err)
						return
					}
					servers := make([]util.ServerImpl, 0)
					for ifName, ip := range ifNameAddressMap {
						logrus.Infof("creating %s server for %s:%s", protocol, ifName, ip)
						server, err := (*sm).CreateServer(tg.namespace, tg.podName, tg.nodeName, ip,
							ifName, tg.socketReadBufferSize, tg.promRegistry)
						if err != nil {
							logrus.Errorf("error creating server on ip address %s: %v", ip, err)
							continue
						}
						servers = append(servers, server)
					}
					tg.serversMap[protocol] = servers
				}
				for protocol, c := range protoClients {
					logrus.Infof("available bat client: %s", protocol)
					err = (*c).LoadBatProfileConfig(tg.config.GetProfileMap())
					if err != nil {
						logrus.Errorf("error creating %s client profile config: error %v", protocol, err)
						return
					}
				}
				if tg.netBatPairs != nil {
					logrus.Infof("net bat profile is set. starting tgen clients")
					for _, batPair := range tg.netBatPairs {
						logrus.Infof("bat pair %v, %s, %s, %s", *batPair.Source, batPair.Destination.Name,
							batPair.TrafficProfile, batPair.TrafficScenario)
					}
					tg.createNetBatTgenClients()
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			cm := newObj.(*v1.ConfigMap)
			resourceVersion := cm.GetResourceVersion()
			switch cm.Name {
			case netBatPairing:
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				if resourceVersion == tg.netPairResourceVersion {
					logrus.Infof("no change in net-bat-pairing config map, returning")
					return
				}
				tg.netPairResourceVersion = resourceVersion
				// reconfigure the clients with new pairing config
				tg.deleteNetBatTgenClients()
				tg.handleNetBatPairingAddEvent(cm)
				return
			case netBatProfile:
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				if resourceVersion == tg.config.GetResourceVersion() {
					logrus.Infof("no change in net-bat-profile config map, returning")
					return
				}
				suspendOld := tg.config.SuspendTraffic()
				_, err := util.ReLoadConfig(cm, tg.config)
				if err != nil {
					logrus.Errorf("error processing configmap %v: error %v", cm, err)
					return
				}
				for protocol, ps := range protoServers {
					logrus.Infof("update config for bat server: %s", protocol)
					err := (*ps).LoadBatProfileConfig(tg.config.GetProfileMap())
					if err != nil {
						logrus.Errorf("error updating %s profile config: error %v", protocol, err)
						return
					}
				}
				for protocol, pc := range protoClients {
					logrus.Infof("update config for bat client: %s", protocol)
					err := (*pc).LoadBatProfileConfig(tg.config.GetProfileMap())
					if err != nil {
						logrus.Errorf("error updating %s profile config: error %v", protocol, err)
						return
					}
				}
				// In case of only suspend or resume traffic cm update, don't
				// restart the traffic.
				if suspendOld != tg.config.SuspendTraffic() {
					return
				}
				// restart the client with new config settings
				tg.restartNetBatTgenClients()
			}
		},
		DeleteFunc: func(obj interface{}) {
			cm := obj.(*v1.ConfigMap)
			switch cm.Name {
			case netBatPairing:
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				tg.netPairResourceVersion = ""
				tg.deleteNetBatTgenClients()
				return
			case netBatProfile:
				tg.mutex.Lock()
				defer tg.mutex.Unlock()
				tg.stopServers()
				tg.config = nil
				return
			}
		},
	})
	go func() {
		atomic.StoreInt32(&(tg.isRunning), int32(1))
		// informer Run blocks until informer is stopped
		logrus.Infof("starting config map informer")
		informer.Run(tg.stopper)
		logrus.Infof("config map informer is stopped")
		atomic.StoreInt32(&(tg.isRunning), int32(0))
	}()
}

// StopTGC stop listening for config map changes and stop tgc for every pair
func (tg *podTGC) StopTGC() {
	close(tg.stopper)
	tEnd := time.Now().Add(3 * time.Second)
	for tEnd.After(time.Now()) {
		if atomic.LoadInt32(&tg.isRunning) == 0 {
			logrus.Infof("config map informer is no longer running, proceed to tearing down protocol clients and servers")
			break
		}
		time.Sleep(600 * time.Millisecond)
	}
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
			logrus.Infof("bat pair %v, %s, %s, %s", *batPair.Source, batPair.Destination.Name, batPair.TrafficProfile, batPair.TrafficScenario)
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
			if _, exists := protoClients[p.TrafficProfile]; !exists {
				logrus.Errorf("error getting protocol client for pair %v", p)
				return
			}
			client, err := (*protoClients[p.TrafficProfile]).CreateClient(p, tg.socketReadBufferSize, tg.promRegistry)
			if err != nil {
				logrus.Errorf("error creating client for pair %v: %v", p, err)
				return
			}
			p.ClientConnection = client
			err = p.ClientConnection.SetupConnection()
			if err != nil {
				source, _ := json.Marshal(p.Source)
				logrus.Errorf("error in setting up the connection for pair %s-%v-%s-%s: %v", string(source),
					*p.Destination, p.TrafficProfile, p.TrafficScenario, err)
				return
			}
			go p.ClientConnection.HandleTimeouts()
			go p.ClientConnection.SocketRead()
			go p.ClientConnection.StartPackets()
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
	var pairsStr string
	var err error
	if val, ok := cm.Data["net-bat-pairing.cfg"]; ok {
		pairsStr = val
	} else if val, ok := cm.Data["net-bat-pairing.cfg.gz"]; ok {
		sBin, err := b64.StdEncoding.DecodeString(val)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		gr, err := gzip.NewReader(bytes.NewBuffer(sBin))
		if err != nil {
			return nil, err
		}
		defer func() {
			err = gr.Close()
			if err != nil {
				logrus.Errorf("error closing gzip reader: %v", err)
			}
		}()
		sBin, err = ioutil.ReadAll(gr)
		if err != nil {
			return nil, err
		}
		buf.Write(sBin)
		pairsStr = buf.String()
	} else {
		return nil, errors.New("no pairing config present")
	}
	pairings, err := getAvailableNetBatPairings(namespace, podName, pairsStr)
	if err != nil {
		return nil, err
	}
	return pairings, nil
}

func getAvailableNetBatPairings(namespace, podName, pairingStr string) ([]util.BatPair, error) {
	lines := strings.Split(string(pairingStr), "\n")
	pairs := make([]util.BatPair, 0)
	ifaceResponse, err := util.GetInterfaces()
	if err != nil {
		return nil, err
	}
	ifNameAddressMap, err := util.GetNetInterfaces()
	if err != nil {
		return nil, err
	}
	netIfNameMap := make(map[string]string)
	for _, iface := range ifaceResponse.Interface {
		netIfNameMap[iface.NetworkStatus.Name] = iface.NetworkStatus.Interface
	}
	pairMap := make(map[string][]util.BatPair)
	rand.Seed(time.Now().UnixNano())
	for _, line := range lines {
		pair := strings.TrimSpace(line)
		if pair == "" || strings.HasPrefix(pair, "#") {
			continue
		}
		elements := strings.Split(pair, "}\",")
		source := &util.Source{}
		sourceStr := strings.TrimLeft(strings.TrimSpace(elements[0]), "\"") + "}"
		sourceStr = strings.ReplaceAll(sourceStr, "'", "\"")
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
		if source.Net != "" {
			if srcIface, ok := netIfNameMap[namespace+"/"+source.Net]; ok {
				source.Interface = srcIface
				if srcIfaceIPAddress, ok := ifNameAddressMap[srcIface]; ok {
					source.IP = srcIfaceIPAddress
				}
			} else {
				logrus.Errorf("no interface present for given source network %s", source.Net)
			}
		} else if source.Interface != "" {
			if srcIfaceIPAddress, ok := ifNameAddressMap[source.Interface]; ok {
				source.IP = srcIfaceIPAddress
			} else {
				logrus.Errorf("no interface present for given source interface name %s", source.Interface)
			}
		} else if source.IP == "" {
			// assign primary network eth0 interface ip address
			if srcIfaceIPAddress, ok := ifNameAddressMap["eth0"]; ok {
				source.IP = srcIfaceIPAddress
				source.Interface = "eth0"
			} else {
				logrus.Errorf("source doesn't have primary interface in the pair %s, ignoring", pair)
				continue
			}
		}
		elements = trimSlice(strings.Split(elements[1], ","))
		destination := &util.Remote{Name: elements[0]}
		batPair := util.BatPair{Source: source, Destination: destination, TrafficProfile: elements[1], TrafficScenario: elements[2]}
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
		elements = append(elements, strings.Trim(strings.TrimSpace(str), "\""))
	}
	return elements
}
