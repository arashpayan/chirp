package main

import (
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/arashpayan/chirp"
	"github.com/codegangsta/cli"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	app := cli.NewApp()
	app.Name = "chirp"
	app.EnableBashCompletion = true
	app.Usage = "Broadcast and listen to network services using the chirp protocol"
	app.Version = "0.1"

	app.Commands = []cli.Command{
		{
			Name:  "broadcast",
			Usage: "Broadcast a service",
			Action: func(c *cli.Context) error {
				return broadcast(c)
			},
		},
		{
			Name:  "listen",
			Usage: "Listen for a service",
			Action: func(c *cli.Context) error {
				return listen(c)
			},
		},
		{
			Name:  "test",
			Usage: "test",
			Action: func(c *cli.Context) error {
				xnet()
				return nil
			},
		},
	}

	app.Run(os.Args)
}

func xnet() {
	en01, err := net.InterfaceByName("eno1")
	if err != nil {
		log.Fatal(err)
	}

	group := net.IPv4(224, 0, 0, 224)
	conn, err := net.ListenPacket("udp4", "0.0.0.0:6464")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	packetConn := ipv4.NewPacketConn(conn)
	if err := packetConn.JoinGroup(en01, &net.UDPAddr{IP: group}); err != nil {
		log.Fatal(err)
	}
	if err := packetConn.SetControlMessage(ipv4.FlagSrc, true); err != nil {
		log.Fatal(err)
	}

	groupAddr := &net.UDPAddr{IP: group, Port: 6464}
	go listenTo(packetConn)
	for {
		log.Printf("chirping")
		_, err = packetConn.WriteTo([]byte("chirp with x/net"), nil, groupAddr)
		if err != nil {
			log.Fatal(err)
		}
		time.Sleep(1 * time.Second)
	}
}

func listenTo(packetConn *ipv4.PacketConn) {
	buf := make([]byte, 33*1024)
	for {
		num, cm, src, err := packetConn.ReadFrom(buf)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("read: %v from %v. cm: %+v", string(buf[:num]), src, cm)
	}
}

func broadcast(context *cli.Context) error {
	if context.NArg() == 0 {
		return cli.NewExitError("You need to specify a service name", 255)
	}

	_, err := chirp.NewBroadcaster(context.Args().First(), nil)
	if err != nil {
		return cli.NewExitError(err.Error(), 255)
	}

	select {}
}

func listen(context *cli.Context) error {
	// listener, err := chirp.Listen("*")
	// if err != nil {
	// 	return cli.NewExitError(err.Error(), 255)
	// }
	//
	// for {
	// 	msg := <-listener.Messages
	// 	log.Printf("message: %v", msg)
	// }
	return nil
}
