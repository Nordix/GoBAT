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

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Nordix/GoBAT/pkg/tapp"
	"github.com/Nordix/GoBAT/pkg/tgc"
	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// PodName pod name env variable
	PodName = "POD_NAME"
	// NodeName node name env variable
	NodeName = "NODE_NAME"
	// LogFile log file location
	LogFile        = "/var/log/tgc.log"
	defaultCPUProf = "/var/log/tgc.prof"
)

var (
	podName  string
	nodeName string
)

func main() {

	readBufferSize := flag.Int("readbufsize", 1000, "socket read buffer size")
	flag.Parse()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGQUIT)
	done := make(chan bool, 1)

	if err := initializeLog(LogFile); err != nil {
		panic(err)
	}

	goMaxProcs := os.Getenv("GOMAXPROCS")

	if goMaxProcs == "" {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	var exists bool

	podName, exists = os.LookupEnv(PodName)
	if !exists {
		logrus.Errorf("no pod name set in env variable")
		return
	}

	nodeName, exists = os.LookupEnv(NodeName)
	if !exists {
		logrus.Errorf("no node name set in env variable")
		return
	}

	tappServer, err := startTappServer(util.Port, util.ProtocolUDP, readBufferSize)
	if err != nil {
		logrus.Errorf("server connection creation failed: err %v", err)
		return
	}

	// creates the in-cluster config
	clientSet := getClient()

	stopper := make(chan struct{})
	tgController := tgc.NewPodTGController(clientSet, podName, nodeName, readBufferSize, stopper)
	tgController.StartTGC()

	statsCollectTicker := time.NewTicker(5 * time.Second)
	statsCollectStop := make(chan bool, 1)

	go func() {
		for {
			select {
			case <-statsCollectStop:
				return
			case <-statsCollectTicker.C:
				netBatStats, err := tgController.GetAvailableNetBatStats()
				if err != nil {
					continue
				}
				logStats(netBatStats)
			}
		}
	}()

	go func() {
		sig := <-sigs
		logrus.Infof("received the signal %v", sig)
		statsCollectTicker.Stop()
		done <- true
		statsCollectStop <- true
	}()
	// Capture signals to cleanup before exiting
	<-done

	tgController.StopTGC()
	tappServer.TearDownServer()

	logrus.Infof("tgen tapp is stopped")

}

func startTappServer(port int, protocol string, readBufSize *int) (util.ServerImpl, error) {
	server, err := tapp.NewServer(util.Port, util.ProtocolUDP)
	if err != nil {
		return nil, err
	}
	err = server.SetupServerConnection()
	if err != nil {
		return nil, err
	}
	go server.ReadFromSocket(*readBufSize)

	return server, nil
}

// GetClient returns a k8s clientset to the request from inside of cluster
func getClient() kubernetes.Interface {
	config, err := rest.InClusterConfig()
	if err != nil {
		logrus.Errorf("error with retrieving cluster config %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logrus.Errorf("error with configuring kube client %v", err)
	}

	return clientset
}

func logStats(pairStats []util.BatPairStats) {
	var statsString string
	for _, pairStat := range pairStats {
		metric := pairStat.GetTotalMetrics()
		errCode := pairStat.GetErrorCode()
		if errCode == 0 {
			statsString = fmt.Sprintf("%s-%s-%s-%s-%s-%s=> %s-%d-%d-%d-%s", podName, nodeName, pairStat.GetSourceIP(),
				pairStat.GetDestinationIP(), pairStat.GetTrafficCase(), pairStat.GetTrafficType(),
				util.MicroSecToDuration(int(metric.Duration)).Round(1*time.Second), metric.PacketSent, metric.PacketReceived, metric.PacketDropped, util.MicroSecToDuration(int(metric.RoundTrip)).Round(1*time.Microsecond))
		} else {
			statsString = fmt.Sprintf("%s-%s-%s-%s-%s-%s=> %d-%s", podName, nodeName, pairStat.GetSourceIP(),
				pairStat.GetDestinationIP(), pairStat.GetTrafficCase(), pairStat.GetTrafficType(), errCode, pairStat.GetErrorDescription())
		}
		logrus.Infof(statsString)
	}
}

func initializeLog(logFile string) error {
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	mw := io.MultiWriter(os.Stdout, f)
	logrus.SetOutput(mw)
	return nil
}
