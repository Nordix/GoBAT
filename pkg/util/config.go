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
	"fmt"
	"strconv"

	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
)

const (
	defaultUDPPacketSize    = 1000
	defaultUDPPacketTimeout = 5
	defaultUDPSendRate      = 500
	defaultUDPRedialPeriod  = 10
	defaultHTTPSendRate     = 100
)

type config struct {
	profiles         []string
	udpSendRate      int
	udpPacketSize    int
	udpPacketTimeout int
	udpRedialPeriod  int
	httpSendRate     int
	httpQuery        string
	htmlPage         string
}

// Config interface
type Config interface {
	GetProfiles() []string
	GetUDPSendRate() int
	GetUDPPacketSize() int
	GetUDPPacketTimeout() int
	GetUDPRedialPeriod() int
	GetHTTPSendRate() int
	GetHTTPQuery() string
	GetHTMLPage() string
}

// LoadConfig parse the config map and load the config struct
func LoadConfig(cm *v1.ConfigMap) (Config, error) {
	c := config{udpSendRate: defaultUDPSendRate, udpPacketSize: defaultUDPPacketSize,
		udpRedialPeriod: defaultUDPRedialPeriod, udpPacketTimeout: defaultUDPPacketTimeout,
		httpSendRate: defaultHTTPSendRate}
	c.profiles = make([]string, 0)
	yamlMap := make(map[string]map[string]string)
	err := yaml.Unmarshal([]byte(cm.Data["net-bat-profiles.cfg"]), &yamlMap)
	if err != nil {
		return nil, fmt.Errorf("error parsing the config map data %s", cm.Name)
	}

	// Parse UDP profile
	if udpEntry, ok := yamlMap["udp"]; ok {
		c.profiles = append(c.profiles, "udp")
		if val, ok := udpEntry["send-rate"]; ok {
			c.udpSendRate, err = parseIntValue(val)
			if err != nil {
				return nil, fmt.Errorf("parsing udp-send-rate failed: err %v", err)
			}
		}

		if val, ok := udpEntry["packet-size"]; ok {
			c.udpPacketSize, err = parseIntValue(val)
			if err != nil {
				return nil, fmt.Errorf("parsing udp-packet-size failed: err %v", err)
			}
		}

		if val, ok := udpEntry["packet-timeout"]; ok {
			c.udpPacketTimeout, err = parseIntValue(val)
			if err != nil {
				return nil, fmt.Errorf("parsing udp-packet-timeout failed: err %v", err)
			}
		}

		if val, ok := udpEntry["redial-period"]; ok {
			c.udpRedialPeriod, err = parseIntValue(val)
			if err != nil {
				return nil, fmt.Errorf("parsing udp-redial-period failed: err %v", err)
			}
		}
	}

	// Parse HTTP Profile
	if httpEntry, ok := yamlMap["http"]; ok {
		c.profiles = append(c.profiles, "http")
		if val, ok := httpEntry["send-rate"]; ok {
			c.httpSendRate, err = parseIntValue(val)
			if err != nil {
				return nil, fmt.Errorf("parsing http send rate failed: err %v", err)
			}
		}

		if val, ok := httpEntry["http-query"]; ok {
			c.httpQuery = val
		}

		if val, ok := httpEntry["html-page"]; ok {
			c.htmlPage = val
		}
	}

	return &c, nil
}

func (c *config) GetProfiles() []string {
	return c.profiles
}

func (c *config) GetUDPSendRate() int {
	return c.udpSendRate
}

func (c *config) GetUDPPacketSize() int {
	return c.udpPacketSize
}

func (c *config) GetUDPPacketTimeout() int {
	return c.udpPacketTimeout
}

func (c *config) GetUDPRedialPeriod() int {
	return c.udpRedialPeriod
}

func (c *config) GetHTTPSendRate() int {
	return c.httpSendRate
}

func (c *config) GetHTTPQuery() string {
	return c.httpQuery
}
func (c *config) GetHTMLPage() string {
	return c.htmlPage
}

func parseIntValue(value string) (int, error) {
	valueInt, err := strconv.Atoi(value)
	if err != nil {
		return -1, err
	}
	return valueInt, nil
}
