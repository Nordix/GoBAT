package tgen

import (
	"errors"
	"fmt"

	"github.com/Nordix/GoBAT/pkg/util"
)

// NewClient get the client implementation for the given protocol
func NewClient(p *util.BatPair) (util.ClientImpl, error) {
	switch p.TrafficType {
	case util.ProtocolUDP:
		return NewUDPClient(p), nil
	case util.ProtocolHTTP:
		return nil, errors.New("http client not supported")
	default:
		return nil, fmt.Errorf("unknown protocol %s", p.TrafficType)
	}
}
