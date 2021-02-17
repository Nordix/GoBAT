package tapp

import (
	"errors"
	"fmt"

	"github.com/Nordix/GoBAT/pkg/util"
)

// NewServer get the server implementation for the given protocol
func NewServer(port int, protocol string) (util.ServerImpl, error) {
	switch protocol {
	case util.ProtocolUDP:
		return NewUDPServer(port), nil
	case util.ProtocolHTTP:
		return nil, errors.New("http server not supported")
	default:
		return nil, fmt.Errorf("unknown protocol %s", protocol)
	}
}
