package chirp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"time"

	reuse "github.com/jbenet/go-reuseport"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// MaxPayloadBytes is the maximum allowed size of a payload when serialized into
// JSON
const MaxPayloadBytes = 32 * 1024
const maxMessageBytes = 33 * 1024

var ipv4Group = net.IPv4(224, 0, 0, 224)
var ipv6Group = net.ParseIP("FF06::224")

const chirpPort = 6464

var serviceNameRegExp = regexp.MustCompile(`[a-zA-Z0-9\.\-]+`)

type temporaryError struct {
	cause string
}

func (te temporaryError) Error() string {
	return te.cause
}

type broadcasterPayload []byte

func (b broadcasterPayload) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("null"), nil
	}
	return b, nil
}

type messageType string

const (
	messageTypeListenerJoined      messageType = "listener_joined"
	messageTypeServiceAnnouncement             = "service_announcement"
)

func validMessageType(msgType messageType) bool {
	switch msgType {
	case messageTypeListenerJoined:
	default:
		return false
	}

	return true
}

type message struct {
	srcIP        net.IP
	payloadBytes broadcasterPayload

	Type        messageType            `json:"type"`
	SenderID    string                 `json:"sender_id"`
	ServiceName string                 `json:"service_name"`
	Payload     map[string]interface{} `json:"payload"`
	TTL         int                    `json:"ttl"`
}

func (m *message) valid() bool {
	if m == nil {
		return false
	}

	if !validMessageType(m.Type) {
		return false
	}

	if err := ValidateServiceName(m.ServiceName); err != nil {
		return false
	}

	return true
}

func (m message) MarshalJSON() ([]byte, error) {
	jsonMsg := map[string]interface{}{
		"type":         m.Type,
		"sender_id":    m.SenderID,
		"service_name": m.ServiceName,
	}
	if m.payloadBytes != nil {
		jsonMsg["payload"] = m.payloadBytes
	}

	return json.Marshal(jsonMsg)
}

// an abstraction later to help us deal with the differences between ipv4 and
// ipv6 connection differences
type connection struct {
	groupAddr *net.UDPAddr
	v4        *ipv4.PacketConn
	v6        *ipv6.PacketConn
	readBuf   []byte
}

func (c *connection) write(msg interface{}) error {
	buf, err := json.Marshal(msg)
	if err != nil {
		return errors.New("unable to marshall message - " + err.Error())
	}
	if c.v4 != nil {
		c.v4.WriteTo(buf, nil, c.groupAddr)
	} else if c.v6 != nil {
		c.v6.WriteTo(buf, nil, c.groupAddr)
	} else {
		panic("no packet connection found")
	}

	return nil
}

func (c *connection) read() (*message, error) {
	var num int
	var srcIP net.IP
	var err error
	if c.v4 != nil {
		var cm *ipv4.ControlMessage
		num, cm, _, err = c.v4.ReadFrom(c.readBuf)
		srcIP = cm.Src
		// log.Printf("v4 cm: %v", cm)
	} else if c.v6 != nil {
		var cm *ipv6.ControlMessage
		num, cm, _, err = c.v6.ReadFrom(c.readBuf)
		srcIP = cm.Src
		// log.Printf("v6 cm: %v", cm)
	} else {
		panic("no packet connection found")
	}

	if err != nil {
		return nil, errors.New("error reading message - " + err.Error())
	}
	if num > maxMessageBytes {
		return nil, temporaryError{cause: "message was too big"}
	}

	msg := &message{srcIP: srcIP}
	err = json.Unmarshal(c.readBuf[:num], msg)
	if err != nil {
		return nil, errors.New("received corrupt message - " + err.Error())
	}

	return msg, nil
}

func (c *connection) close() error {
	if c.v4 != nil {
		return c.v4.Close()
	} else if c.v6 != nil {
		return c.v6.Close()
	} else {
		panic("no packet connection found")
	}
}

func newConnection() connection {
	return connection{readBuf: make([]byte, maxMessageBytes)}
}

func newIP4Connection() (*connection, error) {
	// conn, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", chirpPort))
	conn, err := reuse.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", chirpPort))
	if err != nil {
		return nil, err
	}
	log.Printf("newIP4 local: %v", conn.LocalAddr())

	packetConn := ipv4.NewPacketConn(conn)
	if err := packetConn.JoinGroup(nil, &net.UDPAddr{IP: ipv4Group}); err != nil {
		return nil, errors.New("unable to join chirp multicast group - " + err.Error())
	}
	if err := packetConn.SetControlMessage(ipv4.FlagSrc, true); err != nil {
		return nil, errors.New("unable to set control message ipv4.FlagSrc - " + err.Error())
	}
	return &connection{
		readBuf:   make([]byte, maxMessageBytes),
		v4:        packetConn,
		groupAddr: &net.UDPAddr{IP: ipv4Group, Port: chirpPort},
	}, nil
}

func newIP6Connection() (*connection, error) {
	conn, err := reuse.ListenPacket("udp6", fmt.Sprintf("[::]:%d", chirpPort))
	if err != nil {
		return nil, err
	}
	log.Printf("newIP6 local: %v", conn.LocalAddr())

	packetConn := ipv6.NewPacketConn(conn)
	if err := packetConn.JoinGroup(nil, &net.UDPAddr{IP: ipv6Group}); err != nil {
		return nil, errors.New("unable to join chirp multicast group - " + err.Error())
	}
	// Why do I have to specify ipv6.FlagDst to get the Src address on each packet?
	if err := packetConn.SetControlMessage(ipv6.FlagDst, true); err != nil {
		return nil, errors.New("unable to set control message ipv6.FlagDst: " + err.Error())
	}
	return &connection{
		readBuf:   make([]byte, maxMessageBytes),
		v6:        packetConn,
		groupAddr: &net.UDPAddr{IP: ipv6Group, Port: chirpPort},
	}, nil
}

// Broadcaster ...
type Broadcaster struct {
	id         string
	service    string
	payload    broadcasterPayload
	serviceTTL int
	v4Conn     *connection
	v6Conn     *connection
}

// ValidateServiceName ...
func ValidateServiceName(name string) error {
	if len(name) == 0 {
		return errors.New("service names can not be empty")
	}
	if len(name) > 64 {
		return errors.New("service names may not be longer than 64 bytes")
	}
	if serviceNameRegExp.FindString(name) != name {
		return errors.New("service names can only contain a-z, A-Z, 0-9, . (period) or - (hyphen)")
	}

	return nil
}

// NewBroadcaster ...
func NewBroadcaster(service string, payload map[string]interface{}) (*Broadcaster, error) {
	if err := ValidateServiceName(service); err != nil {
		return nil, err
	}

	var serialized []byte
	if payload != nil {
		var err error
		serialized, err = json.Marshal(payload)
		if err != nil {
			return nil, errors.New("unable to convert payload into json - " + err.Error())
		}
		if len(serialized) > MaxPayloadBytes {
			return nil, fmt.Errorf("payload too large (%d bytes); must be smaller than 32KB after serialization", len(serialized))
		}
	}

	b := &Broadcaster{
		service:    service,
		payload:    serialized,
		id:         randHexaDecimal(32),
		serviceTTL: 60,
	}
	var err error
	b.v4Conn, err = newIP4Connection()
	if err != nil {
		b.Stop()
		return nil, fmt.Errorf("unable to v4 multicast broadcast - %v", err)
	}
	b.v6Conn, err = newIP6Connection()
	if err != nil {
		b.v4Conn.close()
		return nil, fmt.Errorf("unable to v6 multicast broadcast - %v", err)
	}

	go b.serve(b.v4Conn)
	go b.serve(b.v6Conn)

	return b, nil
}

func (b *Broadcaster) serve(conn *connection) {
	announceMsg := message{
		Type:         messageTypeServiceAnnouncement,
		SenderID:     b.id,
		ServiceName:  b.service,
		payloadBytes: b.payload,
		TTL:          60,
	}
	err := conn.write(announceMsg)
	if err != nil {
		if tmpErr, ok := err.(temporaryError); !ok {
			log.Fatal(tmpErr)
		} else {
			log.Printf("temporary write error - %v", tmpErr)
		}
	}

	received := make(chan *message)
	go read(conn, received)
	announce := make(chan bool)
	go func() {
		for {
			time.Sleep(1 * time.Second)
			announce <- true
		}
	}()

	for {
		select {
		case <-announce:
			conn.write(announceMsg)
		case msg := <-received:
			if msg.SenderID == b.id {
				// ignore messages we have sent
				continue
			}
			log.Printf("on: %+v", msg)
		}
	}
}

func read(conn *connection, msgs chan<- *message) {
	for {
		msg, err := conn.read()
		if err != nil {
			log.Print(err)
			continue
		}
		msgs <- msg
	}
}

// Stop ...
func (b *Broadcaster) Stop() {

}

// Service ...
type Service struct {
	broadcasterID  string
	v4IP           net.IP
	v4IPExpiration time.Time
	v6IP           net.IP
	v6IPExpiration time.Time
	Name           string
	Payload        map[string]interface{}
	expirationTime time.Time
}

func (s Service) String() string {
	tmp := map[string]interface{}{
		"BroadcasterID": s.broadcasterID,
		"Name":          s.Name,
		"Payload":       s.Payload,
		"TTL":           s.expirationTime.Unix(),
		"V4":            s.v4IP,
		"V4TTL":         s.v4IPExpiration.Unix(),
		"V6":            s.v6IP,
		"V6TTL":         s.v6IPExpiration.Unix(),
	}
	buf, _ := json.Marshal(tmp)
	return string(buf)
}

// Listener ...
type Listener struct {
	id          string
	v4Conn      *connection
	v6Conn      *connection
	serviceName string
	// broadcaster id => Service
	knownServices map[string]Service
	discovered    chan Service
	Discovered    <-chan Service
	updated       chan Service
	Updated       <-chan Service
	removed       chan Service
	Removed       <-chan Service
}

func (l *Listener) listen(conn *connection) {
	// announce our presence to the group
	helloMsg := message{
		Type:     messageTypeListenerJoined,
		SenderID: l.id,
	}
	conn.write(helloMsg)

	received := make(chan *message)
	go read(conn, received)
	for {
		select {
		case msg := <-received:
			// log.Printf("on: %+v", msg)
			// ignore our own messages
			if msg.SenderID == l.id {
				continue
			}
			switch msg.Type {
			case messageTypeServiceAnnouncement:
				l.handleAnnouncement(msg)
			}
		}
	}
}

func (l *Listener) handleAnnouncement(msg *message) {
	// Is this a service that we're interested in?
	if l.serviceName != "*" {
		if msg.ServiceName != l.serviceName {
			return
		}
	}
	// check if we have a record for this service already
	// log.Printf("srcIP: %s", msg.srcIP)
	service, ok := l.knownServices[msg.SenderID]
	ttl := time.Now().Add(time.Duration(msg.TTL) * time.Second)
	service.expirationTime = ttl
	if !ok { // this is the first time we've seen this service
		if msg.srcIP.To4() != nil {
			// log.Printf("initing with v4")
			service.v4IP = msg.srcIP
			service.v4IPExpiration = ttl
		} else {
			// log.Printf("initing with v6")
			service.v6IP = msg.srcIP
			service.v6IPExpiration = ttl
		}
		service.Name = msg.ServiceName
		service.broadcasterID = msg.SenderID
		service.Payload = msg.Payload
		l.discovered <- service
	} else { // we've seen this service before. check if we have a new ip address
		updatedIP := false
		if msg.srcIP.To4() != nil {
			service.v4IPExpiration = ttl
			// log.Printf("v4 ttl")
			if service.v4IP == nil {
				// log.Printf("setting v4")
				service.v4IP = msg.srcIP
				updatedIP = true
			} else {
				if !service.v4IP.Equal(msg.srcIP) {
					// log.Printf("updating v4")
					service.v4IP = msg.srcIP
					updatedIP = true
				}
			}
		} else {
			service.v6IPExpiration = ttl
			// log.Printf("v6 ttl")
			if service.v6IP == nil {
				// log.Printf("setting v6: %v", msg.srcIP)
				service.v6IP = msg.srcIP
				updatedIP = true
			} else {
				if !service.v6IP.Equal(msg.srcIP) {
					// log.Printf("updating v6")
					service.v6IP = msg.srcIP
					updatedIP = true
				}
			}
		}
		if updatedIP {
			l.updated <- service
		}
	}
	// log.Printf("service: %v", service)
	l.knownServices[msg.SenderID] = service
}

// NewListener ...
func NewListener(serviceName string) (*Listener, error) {
	if serviceName != "*" {
		if err := ValidateServiceName(serviceName); err != nil {
			return nil, err
		}
	}

	l := &Listener{
		id:            randHexaDecimal(32),
		serviceName:   serviceName,
		knownServices: make(map[string]Service),
		discovered:    make(chan Service),
		updated:       make(chan Service),
	}
	// initialize the read only version of the channels for the end user
	l.Discovered = l.discovered
	l.Updated = l.updated

	var err error
	l.v4Conn, err = newIP4Connection()
	if err != nil {
		return nil, fmt.Errorf("unable to v4 multicast listen - %v", err)
	}
	l.v6Conn, err = newIP6Connection()
	if err != nil {
		l.v4Conn.close()
		return nil, fmt.Errorf("unable to v6 multicast listen - %v", err)
	}

	go l.listen(l.v4Conn)
	go l.listen(l.v6Conn)

	return l, nil
}
