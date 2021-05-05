// Copyright (C) 2021, Nordix Foundation
//
// All rights reserved. This program and the accompanying materials
// are made available under the terms of the Apache License, Version 2.0
// which accompanies this distribution, and is available at
// http://www.apache.org/licenses/LICENSE-2.0

package tgc

import (
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

var (
	protoServers = make(map[string]*util.ProtocolServerModule)
	protoClients = make(map[string]*util.ProtocolClientModule)
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

func RegiserProtocolServer(protocol string, server util.ProtocolServerModule) {
	protoServers[protocol] = &server
}

func RegiserProtocolClient(protocol string, client util.ProtocolClientModule) {
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
				logrus.Errorf("error in setting up the connection for pair %v: %v", p, err)
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
			}
		} else if source.Interface != "" {
			if srcIfaceIPAddress, ok := ifNameAddressMap[source.Interface]; ok {
				source.IP = srcIfaceIPAddress
			}
		}
		if source.IP == "" {
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
