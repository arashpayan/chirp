package chirp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
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

// chirps have a max size of 33 KB
const maxChirpSize = 33 * 1024

var serviceNameRegExp = regexp.MustCompile(`[a-zA-Z0-9\.\-]+`)

type temporaryError struct {
	cause string
}

func (te temporaryError) Error() string {
	return te.cause
}

// an abstraction later to help us deal with the differences between ipv4 and
// ipv6 connection differences
type connection struct {
	groupAddr *net.UDPAddr
	v4        *ipv4.PacketConn
	v6        *ipv6.PacketConn
	readBuf   []byte
}

func (c *connection) write(msg map[string]interface{}) error {
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
	var srcAddr net.Addr
	var err error
	if c.v4 != nil {
		num, _, srcAddr, err = c.v4.ReadFrom(c.readBuf)
	} else if c.v6 != nil {
		num, _, srcAddr, err = c.v6.ReadFrom(c.readBuf)
	} else {
		panic("no packet connection found")
	}

	if err != nil {
		return nil, errors.New("error reading message - " + err.Error())
	}
	if num > maxMessageBytes {
		return nil, temporaryError{cause: "message was too big"}
	}
	srcIP, _, err := net.SplitHostPort(srcAddr.String())
	if err != nil {
		return nil, temporaryError{cause: "unable to parse source address - " + err.Error()}
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

func newIP4Connection(iface net.Interface) (*connection, error) {
	conn, err := net.ListenPacket("udp4", "0.0.0.0:"+strconv.Itoa(chirpPort))
	if err != nil {
		return nil, err
	}

	packetConn := ipv4.NewPacketConn(conn)
	if err := packetConn.JoinGroup(&iface, &net.UDPAddr{IP: ipv4Group}); err != nil {
		return nil, errors.New("unable to join chirp multicast group - " + err.Error())
	}
	return &connection{
		readBuf:   make([]byte, maxMessageBytes),
		v4:        packetConn,
		groupAddr: &net.UDPAddr{IP: ipv4Group, Port: chirpPort},
	}, nil
}

func newIP6Connection(iface net.Interface) (*connection, error) {
	conn, err := net.ListenPacket("udp6", "[::]:"+strconv.Itoa(chirpPort))
	if err != nil {
		return nil, err
	}

	packetConn := ipv6.NewPacketConn(conn)
	if err := packetConn.JoinGroup(&iface, &net.UDPAddr{IP: ipv6Group}); err != nil {
		return nil, errors.New("unable to join chirp multicast group - " + err.Error())
	}
	return &connection{
		readBuf:   make([]byte, maxMessageBytes),
		v6:        packetConn,
		groupAddr: &net.UDPAddr{IP: ipv6Group, Port: chirpPort},
	}, nil
}

type broadcasterPayload []byte

func (b broadcasterPayload) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("null"), nil
	}
	return b, nil
}

type message struct {
	srcIP       string
	ServiceName string                 `json:"service_name"`
	Payload     map[string]interface{} `json:"payload"`
}

func (m *message) valid() bool {
	if m == nil {
		return false
	}

	if err := ValidateServiceName(m.ServiceName); err != nil {
		return false
	}

	return true
}

// Broadcaster ...
type Broadcaster struct {
	handlers []interfaceHandler
	service  string
	payload  broadcasterPayload
	v4Conn   *net.UDPConn
	v6Conn   *net.UDPConn
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

	b := &Broadcaster{service: service, payload: serialized}
	ifaces, err := net.Interfaces()
	if err != nil {
		// Long Term: Why can this fail? Can we handle it better?
		return nil, err
	}
	for _, iface := range ifaces {
		if iface.Name == "lo" {
			continue
		}
		v4, err := newIP4Connection(iface)
		// v4, err := newIP4Conn(iface)
		if err != nil {
			// what should we do here?
			b.Stop()
			return nil, fmt.Errorf("unable to v4 multicast on %s - %v", iface.Name, err)
		}
		v6, err := newIP6Connection(iface)
		// v6, err := newIP6Conn(iface)
		if err != nil {
			v4.close()
			b.Stop()
			return nil, fmt.Errorf("unable to v6 multicast on %s - %v", iface.Name, err)
		}
		handler := interfaceHandler{
			iface:  iface,
			v6Conn: v6,
			v4Conn: v4,
		}
		b.handlers = append(b.handlers, handler)
		go b.serve(handler.v4Conn)
		go b.serve(handler.v6Conn)
	}

	return b, nil
}

func (b *Broadcaster) serve(conn *connection) {
	m := map[string]interface{}{
		"service_name": b.service,
		"payload":      b.payload,
	}
	err := conn.write(m)
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
			log.Printf("about to follow up")
			conn.write(m)
		case msg := <-received:
			log.Printf("on: %v", msg)
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

type interfaceHandler struct {
	iface  net.Interface
	v4Conn *connection
	v6Conn *connection
}
