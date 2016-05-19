package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/arashpayan/chirp"
	"github.com/codegangsta/cli"
)

var publisher *chirp.Publisher

func init() {
	systemSigChan := make(chan os.Signal, 1)
	signal.Notify(systemSigChan, syscall.SIGTERM)
	signal.Notify(systemSigChan, syscall.SIGINT)
	go func() {
		<-systemSigChan
		// log.Printf("Signal %v. Exiting", sig)
		if publisher != nil {
			publisher.Stop()
		}
		os.Exit(0)
	}()
}

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
	ip := net.ParseIP("fe80::92c1:4f30:dba:c8b8")
	log.Printf("ip: %v", ip)
	log.Printf("global unicast?: %v", ip.IsGlobalUnicast())
	log.Printf("interface local multicast?: %v", ip.IsInterfaceLocalMulticast())
	log.Printf("link local multicast?: %v", ip.IsLinkLocalMulticast())
	log.Printf("link local unicast?: %v", ip.IsLinkLocalUnicast())
	log.Printf("loopback?: %v", ip.IsLoopback())
	log.Printf("multicast?: %v", ip.IsMulticast())
	log.Printf("unspecified?: %v", ip.IsUnspecified())

}

func broadcast(context *cli.Context) error {
	if context.NArg() == 0 {
		return cli.NewExitError("You need to specify a service name", 255)
	}

	var err error
	publisher, err = chirp.NewPublisher(context.Args().First()).SetTTL(10).Start()
	if err != nil {
		return cli.NewExitError(err.Error(), 255)
	}

	select {}
}

func listen(context *cli.Context) error {
	var serviceName string
	if context.NArg() == 0 {
		serviceName = "*"
	} else {
		serviceName = context.Args().First()
	}
	listener, err := chirp.NewListener(serviceName)
	if err != nil {
		return cli.NewExitError(err.Error(), 255)
	}

	for {
		select {
		case s := <-listener.Discovered:
			log.Printf("discovered: %v", s)
		case s := <-listener.Updated:
			log.Printf("updated: %v", s)
		case s := <-listener.Removed:
			log.Printf("removed: %v", s)
		}
	}
}
