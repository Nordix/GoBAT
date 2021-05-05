// Copyright (C) 2021, Nordix Foundation
//
// All rights reserved. This program and the accompanying materials
// are made available under the terms of the Apache License, Version 2.0
// which accompanies this distribution, and is available at
// http://www.apache.org/licenses/LICENSE-2.0

package util

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
)

type config struct {
	suspendTraffic bool
}

// Config interface
type Config interface {
	SuspendTraffic() bool
}

func LoadConfig(cm *v1.ConfigMap) (Config, error) {
	c := config{suspendTraffic: false}
	return ReLoadConfig(cm, &c)
}

// ReLoadConfig parse the config map and load the config struct
func ReLoadConfig(cm *v1.ConfigMap, c interface{}) (Config, error) {
	rc := c.(*config)
	yamlMap := make(map[string]map[string]string)
	err := yaml.Unmarshal([]byte(cm.Data["net-bat-profiles.cfg"]), &yamlMap)
	if err != nil {
		return nil, fmt.Errorf("error parsing the config map data %s", cm.Name)
	}
	// parse Common profile parameters
	if commonEntry, ok := yamlMap["common"]; ok {
		if val, ok := commonEntry["suspend-traffic"]; ok {
			rc.suspendTraffic, err = strconv.ParseBool(val)
			if err != nil {
				return nil, fmt.Errorf("parsing suspend-traffic failed: err %v", err)
			}
		}
	}
	return rc, nil
}

func (c *config) SuspendTraffic() bool {
	return c.suspendTraffic
}
