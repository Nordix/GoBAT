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

	"github.com/Nordix/GoBAT/pkg/tgc"
	"github.com/Nordix/GoBAT/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// PodName pod name env variable
	PodName = "POD_NAME"
	// NodeName node name env variable
	NodeName = "NODE_NAME"
	// Namespace pod namespace env variable
	Namespace = "NAMESPACE"
	// LogFile log file location
	LogFile = "/var/log/tgc.log"
)

var (
	tgentappver string
	showVersion bool
)

func main() {

	readBufferSize := flag.Int("readbufsize", 1000, "socket read buffer size")
	promPort := flag.Int("promport", 2212, "prometheus http endpoint port number")
	flag.BoolVar(&showVersion, "version", false, "print version format=<tag>-<git-commit-id-short>")
	flag.Parse()

	if showVersion {
		fmt.Printf("%s\n", tgentappver)
		os.Exit(0)
	}

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

	podName, exists := os.LookupEnv(PodName)
	if !exists {
		logrus.Errorf("no pod name set in env variable")
		return
	}

	nodeName, exists := os.LookupEnv(NodeName)
	if !exists {
		logrus.Errorf("no node name set in env variable")
		return
	}

	namespace, exists := os.LookupEnv(Namespace)
	if !exists {
		logrus.Errorf("no namespace set in env variable")
		return
	}

	reg := prometheus.NewRegistry()
	go util.RegisterPromHandler(*promPort, reg)

	// creates the in-cluster config
	clientSet := getClient()

	stopper := make(chan struct{})
	tgController := tgc.NewPodTGController(clientSet, podName, nodeName, namespace, readBufferSize, stopper, reg)
	tgController.StartTGC()

	go func() {
		sig := <-sigs
		logrus.Infof("received the signal %v", sig)
		done <- true
	}()
	// Capture signals to cleanup before exiting
	<-done

	tgController.StopTGC()

	logrus.Infof("tgen tapp is stopped")

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

func initializeLog(logFile string) error {
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	mw := io.MultiWriter(os.Stdout, f)
	logrus.SetFormatter(&logrus.TextFormatter{TimestampFormat: time.StampMicro})
	logrus.SetOutput(mw)
	return nil
}
