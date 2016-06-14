Chirp (Go)
----------

Chirp is a network service discovery protocol. In short, it's a simpler and more reliable alternative to mDNS/Bonjour. For more information about the protocol and libraries for different languages, see the [official Chirp homepage](https://chirp.arashpayan.com).

Installation
------------
Install with:
```
go get github.com/arashpayan/chirp
```

Usage
-----
Publishing a service:
```
// Create and start a publisher
publisher, _ := chirp.NewPublisher("tld.domain.service").Start()

// Stop the publisher when you're done
publisher.Stop()

// Optionally, you can specify a map with arbitrary
// keys and values that will be broadcasted along
// with your service.
payload := map[string]interface{}{
    "port": 22,
    "display_name": "My Awesome SSH"
    "anotherkey": []{"fizz", "buzz"},
    "andanotherkey": map[string]{}{
        "something": "blah",
        "else": "haha"
    }
}
publisher, _ := chirp.NewPublisher("tld.domain.service").
                      SetPayload(payload).
                      Start()
```

Listening for services
```
// Start the listener. You can pass '*' for the service name
// to listen for all services.
listener, _ := chirp.NewListener("service.to.listen.for")

// Start a goroutine to handle all the incoming service events
for event := range listener.ServiceEvents {
    switch se.EventType {
        case chirp.ServicePublished:
            // handle a newly found service
        case chirp.ServiceUpdated:
            // one of the IPs (v4 or v6) was updated on the service
        case chirp.ServiceRemoved:
            // the service is no longer being published
    }
}

// Stop the listener when you're done
listener.Stop()
```
