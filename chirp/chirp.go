package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/arashpayan/chirp"
	"github.com/urfave/cli"
)

var publisher *chirp.Publisher

func init() {
	systemSigChan := make(chan os.Signal, 1)
	signal.Notify(systemSigChan, syscall.SIGTERM)
	signal.Notify(systemSigChan, syscall.SIGINT)
	go func() {
		<-systemSigChan
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
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "payload,p",
					Usage: "JSON string representing the service payload",
				},
				cli.IntFlag{
					Name:  "ttl",
					Value: 60,
					Usage: "Time to live of the service. You probably don't need to change this."},
			},
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
}

func broadcast(context *cli.Context) error {
	if context.NArg() == 0 {
		return cli.NewExitError("You need to specify a service name", 255)
	}

	publisher := chirp.NewPublisher(context.Args().First())
	// check for a payload
	if context.String("payload") != "" {
		log.Printf("payload: %v", context.String("payload"))
		jsonStr := context.String("payload")
		payload := make(map[string]interface{})
		err := json.Unmarshal([]byte(jsonStr), &payload)
		if err != nil {
			return cli.NewExitError("Unable to parse payload JSON: "+err.Error(), 255)
		}
		publisher.SetPayload(payload)
	}
	ttl := context.Int("ttl")
	if ttl < 0 {
		return cli.NewExitError("TTL must be a positive integer", 255)
	}
	publisher.SetTTL(uint(ttl))

	_, err := publisher.Start()
	if err != nil {
		return cli.NewExitError(err.Error(), 255)
	}
	fmt.Printf("Published '%s'...\n", context.Args().First())

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

	if serviceName == "*" {
		fmt.Println("Listening for all services...")
	} else {
		fmt.Printf("Listening for '%s' services...", serviceName)
	}

	for se := range listener.ServiceEvents {
		switch se.EventType {
		case chirp.ServicePublished:
			var ip4 string
			var ip6 string
			if se.Service.IPv4() != nil {
				ip4 = se.Service.IPv4().String()
			}
			if se.Service.IPv6() != nil {
				ip6 = se.Service.IPv6().String()
			}
			fmt.Printf("+ %s\tIPv4: %s \tIPv6: %s\n", se.Service.Name, ip4, ip6)
		case chirp.ServiceRemoved:
			var ip4 string
			var ip6 string
			if se.Service.IPv4() != nil {
				ip4 = se.Service.IPv4().String()
			}
			if se.Service.IPv6() != nil {
				ip6 = se.Service.IPv6().String()
			}
			fmt.Printf("- %s\tIPv4: %s \tIPv6: %s\n", se.Service.Name, ip4, ip6)
		case chirp.ServiceUpdated:
			var ip4 string
			var ip6 string
			if se.Service.IPv4() != nil {
				ip4 = se.Service.IPv4().String()
			}
			if se.Service.IPv6() != nil {
				ip6 = se.Service.IPv6().String()
			}
			fmt.Printf("| %s\tIPv4: %s \tIPv6: %s\n", se.Service.Name, ip4, ip6)
		}
	}

	return nil
}
