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

type config struct {
	udpSendRate      int
	udpPacketSize    int
	udpPacketTimeout int
	httpSendRate     int
	httpQuery        string
	htmlPage         string
}

// Config interface
type Config interface {
	GetUDPSendRate() int
	GetUDPPacketSize() int
	GetUDPPacketTimeout() int
	GetHTTPSendRate() int
	GetHTTPQuery() string
	GetHTMLPage() string
}

// LoadConfig parse the config map and load the config struct
func LoadConfig(cm *v1.ConfigMap) (Config, error) {
	var c config
	yamlMap := make(map[string]map[string]string)
	err := yaml.Unmarshal([]byte(cm.Data["net-bat-profiles.cfg"]), &yamlMap)
	if err != nil {
		return nil, fmt.Errorf("error parsing the config map data %s", cm.Name)
	}
	if val, ok := yamlMap["udp"]["send-rate"]; ok {
		c.udpSendRate, err = parseIntValue(val)
		if err != nil {
			return nil, fmt.Errorf("parsing udp-send-rate failed: err %v", err)
		}
	}

	if val, ok := yamlMap["udp"]["packet-size"]; ok {
		c.udpPacketSize, err = parseIntValue(val)
		if err != nil {
			return nil, fmt.Errorf("parsing udp-packet-size failed: err %v", err)
		}
	}

	if val, ok := yamlMap["udp"]["packet-timeout"]; ok {
		c.udpPacketTimeout, err = parseIntValue(val)
		if err != nil {
			return nil, fmt.Errorf("parsing udp-packet-timeout failed: err %v", err)
		}
	}

	if val, ok := yamlMap["http"]["send-rate"]; ok {
		c.httpSendRate, err = parseIntValue(val)
		if err != nil {
			return nil, fmt.Errorf("parsing http send rate failed: err %v", err)
		}
	}

	if val, ok := yamlMap["http"]["http-query"]; ok {
		c.httpQuery = val
	}

	if val, ok := yamlMap["http"]["html-page"]; ok {
		c.htmlPage = val
	}

	return &c, nil
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
