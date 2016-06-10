package chirp

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"time"

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

type publisherPayload []byte

func (b publisherPayload) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("null"), nil
	}
	return b, nil
}

type messageType string

const (
	messageTypeNewListener    messageType = "new_listener"
	messageTypePublishService             = "publish"
	messageTypeRemoveService              = "remove_service"
)

type message struct {
	srcIP        net.IP
	payloadBytes publisherPayload

	Type        messageType            `json:"type"`
	SenderID    string                 `json:"sender_id"`
	ServiceName string                 `json:"service_name"`
	Payload     map[string]interface{} `json:"payload"`
	TTL         uint                   `json:"ttl"`
}

func (m *message) valid() error {
	if m == nil {
		return errors.New("message is nil")
	}

	idBytes, err := hex.DecodeString(m.SenderID)
	if err != nil {
		return errors.New("unable to decode 'sender_id' from hex")
	}
	if len(idBytes) != 16 {
		return fmt.Errorf("'sender_id' must be 16 bytes long (found %d)", len(idBytes))
	}
	if m.ServiceName == "" {
		return fmt.Errorf("'service_name' is missing")
	}

	switch m.Type {
	case messageTypeNewListener:
		// wildcard is acceptable for listeners
		if m.ServiceName != "*" {
			if err := ValidateServiceName(m.ServiceName); err != nil {
				return err
			}
		}
	case messageTypePublishService:
		if err := ValidateServiceName(m.ServiceName); err != nil {
			return err
		}
		if m.TTL < 10 {
			return errors.New("ttl must be at least 10 seconds")
		}
	case messageTypeRemoveService:
		if err := ValidateServiceName(m.ServiceName); err != nil {
			return err
		}
	default:
		// unknown message type
		return errors.New("unknown message type")
	}

	return nil
}

func (m message) MarshalJSON() ([]byte, error) {
	jsonMsg := map[string]interface{}{
		"type":         m.Type,
		"sender_id":    m.SenderID,
		"service_name": m.ServiceName,
	}
	switch m.Type {
	case messageTypeNewListener:
	case messageTypeRemoveService:
	case messageTypePublishService:
		jsonMsg["ttl"] = m.TTL
		if m.payloadBytes != nil {
			jsonMsg["payload"] = m.payloadBytes
		}
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
		return errors.New("unable to marshal message - " + err.Error())
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
		if err == nil {
			srcIP = cm.Src
		}
	} else if c.v6 != nil {
		var cm *ipv6.ControlMessage
		num, cm, _, err = c.v6.ReadFrom(c.readBuf)
		if err == nil {
			srcIP = cm.Src
		}
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
		return nil, temporaryError{cause: "received corrupt message - " + err.Error()}
	}

	if err := msg.valid(); err != nil {
		return nil, temporaryError{cause: "received an invalid message: " + err.Error()}
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

func newIP4Connection() (*connection, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipv4Group, Port: chirpPort})
	if err != nil {
		return nil, err
	}

	packetConn := ipv4.NewPacketConn(conn)
	ifaces, err := net.Interfaces()
	if err != nil {
		conn.Close()
		return nil, errors.New("unable to retrieve interfaces - " + err.Error())
	}
	// join the group on any available interfaces
	errCount := 0
	mcastInterfaces := 0
	for _, iface := range ifaces {
		if iface.Flags&net.FlagMulticast != net.FlagMulticast {
			continue
		}
		mcastInterfaces++
		if err := packetConn.JoinGroup(&iface, &net.UDPAddr{IP: ipv4Group}); err != nil {
			errCount++
		}
	}
	if mcastInterfaces == 0 {
		conn.Close()
		return nil, errors.New("no multicast network interfaces available")
	}
	if errCount == mcastInterfaces {
		conn.Close()
		return nil, errors.New("unable to join any multicast interfaces")
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
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: ipv6Group, Port: chirpPort})
	if err != nil {
		return nil, err
	}

	packetConn := ipv6.NewPacketConn(conn)
	ifaces, err := net.Interfaces()
	if err != nil {
		conn.Close()
		return nil, errors.New("unable to retrieve interfaces - " + err.Error())
	}

	// join the group on any available interfaces
	errCount := 0
	mcastInterfaces := 0
	for _, iface := range ifaces {
		if iface.Flags&net.FlagMulticast != net.FlagMulticast {
			continue
		}
		mcastInterfaces++
		if err := packetConn.JoinGroup(&iface, &net.UDPAddr{IP: ipv6Group}); err != nil {
			errCount++
		}
	}
	if mcastInterfaces == 0 {
		conn.Close()
		return nil, errors.New("no multicast network interfaces available")
	}
	if errCount == mcastInterfaces {
		conn.Close()
		return nil, errors.New("unable to join any multicast interfaces")
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

// Publisher ...
type Publisher struct {
	id         string
	service    string
	payload    publisherPayload
	serviceTTL uint
	v4Conn     *connection
	v6Conn     *connection
	initErr    error
	stop       chan bool
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

// NewPublisher ...
func NewPublisher(service string) *Publisher {
	p := &Publisher{
		id:         randSenderID(),
		service:    service,
		serviceTTL: 60,
		stop:       make(chan bool),
	}

	if err := ValidateServiceName(service); err != nil {
		p.initErr = err
	}

	return p
}

// SetTTL ..
func (p *Publisher) SetTTL(ttl uint) *Publisher {
	if p.initErr != nil {
		return p
	}

	// make sure the minimum ttl is 10 seconds
	if ttl < 10 {
		p.initErr = errors.New("TTL must be at least 10 seconds")
	} else {
		p.serviceTTL = ttl
	}

	return p
}

// SetPayload ...
func (p *Publisher) SetPayload(payload map[string]interface{}) *Publisher {
	if p.initErr != nil {
		return p
	}

	var serialized []byte
	if payload != nil {
		var err error
		serialized, err = json.Marshal(payload)
		if err != nil {
			p.initErr = errors.New("unable to convert payload into json - " + err.Error())
			return p
		}
		if len(serialized) > MaxPayloadBytes {
			p.initErr = fmt.Errorf("payload too large (%d bytes); must be smaller than 32KB after serialization", len(serialized))
			return p
		}
	}

	p.payload = serialized

	return p
}

// Start ...
func (p *Publisher) Start() (*Publisher, error) {
	if p.initErr != nil {
		return p, p.initErr
	}

	var err error
	p.v4Conn, err = newIP4Connection()
	if err != nil {
		p.Stop()
		return p, fmt.Errorf("unable to v4 multicast broadcast - %v", err)
	}
	p.v6Conn, err = newIP6Connection()
	if err != nil {
		p.v4Conn.close()
		return p, fmt.Errorf("unable to v6 multicast broadcast - %v", err)
	}

	go p.serve(p.v4Conn)
	go p.serve(p.v6Conn)

	return p, nil
}

func (p *Publisher) serve(conn *connection) {
	defer conn.close()
	announceMsg := message{
		Type:         messageTypePublishService,
		SenderID:     p.id,
		ServiceName:  p.service,
		payloadBytes: p.payload,
		TTL:          p.serviceTTL,
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
			select {
			case <-time.After(time.Duration(p.serviceTTL-4) * time.Second):
				announce <- true
			case <-p.stop:
				return
			}
		}
	}()

serveloop:
	for {
		select {
		case <-announce:
			conn.write(announceMsg)
		case msg := <-received:
			if msg.SenderID == p.id {
				// ignore messages we have sent
				continue
			}
			switch msg.Type {
			case messageTypeNewListener:
				if msg.ServiceName == "*" || msg.ServiceName == p.service {
					conn.write(announceMsg)
				}
			}
		case <-p.stop:
			goodbyeMsg := message{
				Type:        messageTypeRemoveService,
				SenderID:    p.id,
				ServiceName: p.service,
			}
			conn.write(goodbyeMsg)
			break serveloop
		}
	}
}

// Stop ...
func (p *Publisher) Stop() {
	// closing this channel notifies our serving goroutines to clean up
	close(p.stop)
	// give our goroutines enough time to send out service removal messages
	time.Sleep(50 * time.Millisecond)
}

func read(conn *connection, msgs chan<- *message) {
	for {
		msg, err := conn.read()
		if err != nil {
			// if this is not a transient error, then we need to get out of here
			if _, ok := err.(temporaryError); !ok {
				log.Print(err)
				close(msgs)
				return
			}
			log.Print("temp err: " + err.Error())
			continue
		}

		msgs <- msg
	}
}

// Service ...
type Service struct {
	publisherID    string
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
		"PublisherID": s.publisherID,
		"Name":        s.Name,
		"Payload":     s.Payload,
		"TTL":         s.expirationTime.Unix(),
		"V4":          s.v4IP,
		"V4TTL":       s.v4IPExpiration.Unix(),
		"V6":          s.v6IP,
		"V6TTL":       s.v6IPExpiration.Unix(),
	}
	buf, _ := json.Marshal(tmp)
	return string(buf)
}

// IPv4 returns the IPv4 address of the service publisher. If there is no
// v4 IP associated with the publisher, returns nil.
func (s Service) IPv4() net.IP {
	if s.v4IP == nil {
		return nil
	}

	return s.v4IP.To4()
}

// IPv6 returns the IPv6 address of the service publisher. If there is no
// v6 IP associated with the publisher, returns nil.
func (s Service) IPv6() net.IP {
	if s.v6IP == nil {
		return nil
	}

	return s.v6IP.To16()
}

// Listener ...
type Listener struct {
	id          string
	v4Conn      *connection
	v6Conn      *connection
	serviceName string
	// publisher id => Service
	knownServices map[string]Service
	ServiceEvents <-chan ServiceEvent
	serviceEvents chan ServiceEvent
	stop          chan bool
}

// NewListener ...
func NewListener(serviceName string) (*Listener, error) {
	if serviceName != "*" {
		if err := ValidateServiceName(serviceName); err != nil {
			return nil, err
		}
	}

	l := &Listener{
		id:            randSenderID(),
		serviceName:   serviceName,
		stop:          make(chan bool),
		knownServices: make(map[string]Service),
		serviceEvents: make(chan ServiceEvent),
	}
	// initialize the read only version of the channel for the end user
	l.ServiceEvents = l.serviceEvents

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

	// start a goroutine to handle all the incoming messages from both connections
	messageHandler := make(chan *message)
	go func() {
		for {
			select {
			case msg := <-messageHandler:
				// not interested in our own messages
				if msg.SenderID == l.id {
					continue
				}
				switch msg.Type {
				case messageTypePublishService:
					l.handlePublish(msg)
				case messageTypeRemoveService:
					l.handleRemoval(msg)
				}
			case <-l.stop:
				return
			}
		}
	}()

	go l.listen(l.v4Conn, messageHandler)
	go l.listen(l.v6Conn, messageHandler)

	return l, nil
}

func (l *Listener) listen(conn *connection, messageHandler chan<- *message) {
	// announce our presence to the group
	helloMsg := message{
		Type:        messageTypeNewListener,
		SenderID:    l.id,
		ServiceName: l.serviceName,
	}
	conn.write(helloMsg)

	received := make(chan *message)
	go read(conn, received)
	for msg := range received {
		messageHandler <- msg
	}
}

func (l *Listener) handleRemoval(msg *message) {
	// Is this a service that we're interested in?
	if l.serviceName != "*" && msg.ServiceName != l.serviceName {
		return
	}

	// check if we have a record of this service
	service, ok := l.knownServices[msg.SenderID]
	if !ok {
		return
	}
	delete(l.knownServices, msg.SenderID)
	se := ServiceEvent{Service: service, EventType: ServiceRemoved}
	l.serviceEvents <- se
}

func (l *Listener) handlePublish(msg *message) {
	// Is this a service that we're interested in?
	if l.serviceName != "*" && msg.ServiceName != l.serviceName {
		return
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
		service.publisherID = msg.SenderID
		service.Payload = msg.Payload
		se := ServiceEvent{Service: service, EventType: ServicePublished}
		l.serviceEvents <- se
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
			se := ServiceEvent{Service: service, EventType: ServiceUpdated}
			l.serviceEvents <- se
		}
	}
	// log.Printf("service: %v", service)
	l.knownServices[msg.SenderID] = service
}

// Stop listening. The Listener can't be reused after this is called.
func (l *Listener) Stop() {
	close(l.stop)
}

// EventType ...
type EventType string

// events
const (
	ServicePublished EventType = "service_published"
	ServiceUpdated             = "service_updated"
	ServiceRemoved             = "service_removed"
)

// ServiceEvent ...
type ServiceEvent struct {
	Service
	EventType
}
