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
	"errors"
	"fmt"
	"strings"

	"github.com/Nordix/GoBAT/pkg/tgen"
	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const (
	defaultUDPSendRate      = 500
	defaultUDPPacketSize    = 1000
	defaultUDPPacketTimeout = 5
	ppm                     = 1000000
	dropRate100pPPM         = "D = 100%"
	dropRate1pTo100pPPM     = "D < 100%"
	dropRate1000To1pPPM     = "D < 1%"
	dropRate100To1000PPM    = "D < 1000ppm"
	dropRate10To100PPM      = "D < 100ppm"
	dropRate0To10PPM        = "D < 10ppm"
	dropRate0PPM            = "D = 0ppm"
)

// podTGC pod traffic controller
type podTGC struct {
	clientSet            kubernetes.Interface
	socketReadBufferSize int
	config               util.Config
	podName              string
	nodeName             string
	netBatPairs          []util.BatPair
	stopper              chan struct{}
}

// TGController traffic gen controller
type TGController interface {
	StartTGC()
	GetAvailableNetBatStats() ([]util.BatPairStats, error)
	StopTGC()
}

// NewPodTGController creates traffic gen controller for the pod
func NewPodTGController(clientSet kubernetes.Interface, podName, nodeName string, readBufferSize *int, stopper chan struct{}) TGController {
	return &podTGC{clientSet: clientSet, socketReadBufferSize: *readBufferSize, podName: podName, nodeName: nodeName, stopper: stopper}
}

// StartTGC listen for relavant config maps and create pairing
func (tg *podTGC) StartTGC() {
	factory := informers.NewFilteredSharedInformerFactory(tg.clientSet, 0, "", func(o *metav1.ListOptions) {
	})
	informer := factory.Core().V1().ConfigMaps().Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cm := obj.(*v1.ConfigMap)
			switch cm.Name {
			case "net-bat-conf":
				config, batPairs, err := handleAddNetBatConfigMap(cm, tg.podName)
				if err != nil {
					logrus.Errorf("error in parsing configmap %v: error %v", cm, err)
					return
				}
				tg.config = config
				tg.netBatPairs = batPairs
				tg.createNetBatTgenClients()
				return
			case "storage-bat-conf":
				logrus.Errorf("storage bat config not supported")
				return
			case "dpdk-bat-conf":
				logrus.Errorf("dpdk bat config not supported")
				return
			}

		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			//logrus.Infof("update config map not yet supported")
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
			}
		},
	})
	go informer.Run(tg.stopper)
}

// GetAvailableNetBatStats retrieves BatPairStats for each pair
func (tg *podTGC) GetAvailableNetBatStats() ([]util.BatPairStats, error) {
	if tg.netBatPairs == nil {
		return nil, errors.New("no stats available")
	}
	stats := []util.BatPairStats{}
	for index := range tg.netBatPairs {
		stats = append(stats, &tg.netBatPairs[index])
	}
	return stats, nil
}

// StopTGC stop listening for config map changes and stop tgc for every pair
func (tg *podTGC) StopTGC() {
	close(tg.stopper)
	tg.deleteNetBatTgenClients()
}

func (tg *podTGC) createNetBatTgenClients() {
	for index := range tg.netBatPairs {
		go func(p *util.BatPair) {
			p.PendingRequestsMap = make(map[int64]int64)
			p.StartTime = util.GetTimestampMicroSec()
			p.TotalMetrics = util.Metrics{}
			client, err := tgen.NewClient(p)
			if err != nil {
				logrus.Errorf("error creating client for pair %v: %v", p, err)
				p.Err = util.Error{Code: util.TrafficNotStarted, Description: fmt.Sprintf("%v", err)}
				return
			}
			p.ClientConnection = client
			err = p.ClientConnection.SetupConnection()
			if err != nil {
				logrus.Errorf("error in setting up the connection for pair %v: %v", p, err)
				p.Err = util.Error{Code: util.TrafficNotStarted, Description: fmt.Sprintf("%v", err)}
				return
			}
			p.PrometheusRegister()
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
		if pair.ClientConnection == nil || pair.Err.Code == util.TrafficNotStarted {
			continue
		}
		pair.PrometheusUnRegister()
		pair.ClientConnection.TearDownConnection()
	}
	tg.netBatPairs = nil
	tg.config = nil
}

func getAvgDropRateBin(avgDropRate int) string {
	var binStr string
	if avgDropRate <= 0 {
		binStr = dropRate0PPM
	} else if avgDropRate > 0 && avgDropRate < 10 {
		binStr = dropRate0To10PPM
	} else if avgDropRate >= 10 && avgDropRate < 100 {
		binStr = dropRate10To100PPM
	} else if avgDropRate >= 100 && avgDropRate < 1000 {
		binStr = dropRate100To1000PPM
	} else if avgDropRate >= 1000 && avgDropRate < 10000 {
		binStr = dropRate1000To1pPPM
	} else if avgDropRate >= 10000 && avgDropRate < 999900 {
		binStr = dropRate1pTo100pPPM
	} else {
		binStr = dropRate100pPPM
	}
	return binStr
}

func handleAddNetBatConfigMap(cm *v1.ConfigMap, podName string) (util.Config, []util.BatPair, error) {
	config, err := util.LoadConfig(cm)
	if err != nil {
		return nil, nil, err
	}
	pairings := []util.BatPair{}
	if val, ok := cm.Data["pairing"]; ok {
		pairings = getAvailableNetBatPairings(podName, val)
	}
	return config, pairings, nil
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
