package util

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmihailenco/msgpack"
)

const (
	// ProtocolHTTP http protocol string
	ProtocolHTTP = "http"
	// ProtocolUDP udp protocol string
	ProtocolUDP = "udp"
	// MaxBufferSize max buffer size
	MaxBufferSize = 65535
	// Port server port
	Port = 8890
	// PromPort prometheus metric http endpoint port
	PromPort = 2212
	// TrafficNotStarted error code for traffic not started
	TrafficNotStarted = 1
)

const (
	// packetSent represents total packets sent
	packetSent = "packets_sent"
	// packetReceived represents total packets received
	packetReceived = "packets_received"
	// packetDropped represents total packets received
	packetDropped = "packets_dropped"
	// roundTripTime represent total round trip time
	roundTripTime = "total_round_trip_time"
)

// Error error associated with pair
type Error struct {
	Code        int
	Description string
}

// Metrics traffic metrics for each connection
// TODO: to be removed once prometheus metrics started working
type Metrics struct {
	Duration       int64
	PacketSent     int
	PacketReceived int
	PacketDropped  int
	RoundTrip      int64
}

// PrometheusMetrics prometheus metrics for the pair
type PrometheusMetrics struct {
	PacketSent     prometheus.Gauge
	PacketReceived prometheus.Gauge
	PacketDropped  prometheus.Gauge
	RoundTrip      prometheus.Gauge
}

// BatPair represents the BAT traffic to be run between two entities
type BatPair struct {
	SourceIP           string
	DestinationIP      string
	TrafficCase        string
	TrafficProfile     string
	TrafficType        string
	TotalMetrics       Metrics
	PromMetrics        PrometheusMetrics
	PendingRequestsMap map[int64]int64
	StartTime          int64
	ClientConnection   ClientImpl
	Err                Error
}

// Message to be sent and received by protocol clients
type Message struct {
	SequenceNumber   int64
	SendTimeStamp    int64
	RespondTimeStamp int64
	PacketLength     int
}

// Server struct used by protocol server
type Server struct {
	Port int
}

// ServerImpl methods to be implemented by a server
type ServerImpl interface {
	SetupServerConnection() error
	ReadFromSocket(bufSize int)
	TearDownServer()
}

// ClientImpl methods to be implemented by a client
type ClientImpl interface {
	SetupConnection() error
	TearDownConnection()
	SocketRead(bufSize int)
	HandleTimeouts(Config)
	StartPackets(Config)
}

// BatPairStats gettters to retrieve BAT pair in-time statistics
type BatPairStats interface {
	PrometheusRegister()
	PrometheusUnRegister()
	GetSourceIP() string
	GetDestinationIP() string
	GetTrafficCase() string
	GetTrafficProfile() string
	GetTrafficType() string
	GetTotalMetrics() *Metrics
	GetErrorCode() int
	GetErrorDescription() string
}

// GetSourceIP source ip address of the BAT pair
func (bp *BatPair) GetSourceIP() string {
	return bp.SourceIP
}

// GetDestinationIP destination ip address of the BAT pair
func (bp *BatPair) GetDestinationIP() string {
	return bp.DestinationIP
}

// GetTrafficCase traffic case of the BAT pair
func (bp *BatPair) GetTrafficCase() string {
	return bp.TrafficCase
}

// GetTrafficProfile traffic profile of the BAT pair
func (bp *BatPair) GetTrafficProfile() string {
	return bp.TrafficProfile
}

// GetTrafficType traffic type of the BAT pair
func (bp *BatPair) GetTrafficType() string {
	return bp.TrafficType
}

// GetTotalMetrics in time available metrics of the BAT pair
func (bp *BatPair) GetTotalMetrics() *Metrics {
	bp.TotalMetrics.Duration = GetTimestampMicroSec() - bp.StartTime
	return &bp.TotalMetrics
}

// GetErrorCode returns error code (if available) for the BAT pair
func (bp *BatPair) GetErrorCode() int {
	return bp.Err.Code
}

// GetErrorDescription returns err description (if available) for the BAT pair
func (bp *BatPair) GetErrorDescription() string {
	return bp.Err.Description
}

// PrometheusRegister register the BAT pair with Prometheus for metrics export
func (bp *BatPair) PrometheusRegister() {
	label := bp.SourceIP + ":" + bp.DestinationIP + ":" + bp.TrafficType + ":" + bp.TrafficCase
	bp.PromMetrics = PrometheusMetrics{PacketSent: NewGauge(packetSent, "total packet sent", label),
		PacketReceived: NewGauge(packetReceived, "total packet received", label),
		PacketDropped:  NewGauge(packetDropped, "total packet dropped", label),
		RoundTrip:      NewGauge(roundTripTime, "total round trip time", label)}
}

// PrometheusUnRegister unregister the BAT pair from Prometheus
func (bp *BatPair) PrometheusUnRegister() {
	prometheus.Unregister(bp.PromMetrics.PacketSent)
	prometheus.Unregister(bp.PromMetrics.PacketReceived)
	prometheus.Unregister(bp.PromMetrics.PacketDropped)
	prometheus.Unregister(bp.PromMetrics.RoundTrip)
}

// NewMessage creates a new message
func NewMessage(packetLength int, sequence, sendTimeStamp int64) *Message {
	return &Message{SequenceNumber: sequence, SendTimeStamp: sendTimeStamp, RespondTimeStamp: 0, PacketLength: packetLength}
}

// GetPaddingPayload get payload for the given length
func GetPaddingPayload(payloadSize int) ([]byte, error) {
	payload := make([]byte, payloadSize)
	for index := range payload {
		payload[index] = 0xff
	}
	return payload, nil
}

// GetMessageHeaderLength get message header length
func GetMessageHeaderLength() (int, error) {
	msg := Message{SequenceNumber: 0, SendTimeStamp: 0, RespondTimeStamp: 0, PacketLength: 0}
	byteArr, err := msgpack.Marshal(msg)
	if err != nil {
		return -1, err
	}
	return len(byteArr), nil
}

// GetTimestampMicroSec get timestamp in microseconds
func GetTimestampMicroSec() int64 {
	return time.Now().UnixNano() / int64(time.Microsecond)
}

// SecToMicroSec convert given seconds into microseconds
func SecToMicroSec(sec int) int {
	return (sec * int(time.Second) / int(time.Microsecond))
}

// MicroSecToDuration convert give msec into duration
func MicroSecToDuration(msec int) time.Duration {
	return time.Duration(msec) * time.Microsecond
}

// NewGauge creates a new gauge and registers with prometheus
func NewGauge(name, help, label string) prometheus.Gauge {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        name,
		Help:        help,
		ConstLabels: prometheus.Labels{label: label},
	})
	prometheus.MustRegister(gauge)
	return gauge
}
