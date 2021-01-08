/*
Copyright 2018 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// The gnmi_collector program implements a caching gNMI collector.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"sync"
	"time"

	log "github.com/golang/glog"
	"github.com/golang/protobuf/proto"
	"github.com/openconfig/gnmi/cache"
	"github.com/openconfig/gnmi/client"
	gnmiclient "github.com/openconfig/gnmi/client/gnmi"
	coll "github.com/openconfig/gnmi/collector"
	"github.com/openconfig/gnmi/subscribe"
	"github.com/openconfig/gnmi/target"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	cpb "github.com/openconfig/gnmi/proto/collector"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	targetpb "github.com/openconfig/gnmi/proto/target"
)

var (
	configFile           = flag.String("config_file", "", "File path for collector configuration.")
	tunnelConfigFile     = flag.String("tunnel_config_file", "", "File path for tunnel configuration.")
	certFile             = flag.String("cert_file", "", "File path for TLS certificate.")
	keyFile              = flag.String("key_file", "", "File path for TLS key.")
	port                 = flag.Int("port", 0, "server port")
	dialTimeout          = flag.Duration("dial_timeout", time.Minute, "Timeout for dialing a connection to a target.")
	metadataUpdatePeriod = flag.Duration("metadata_update_period", 0, "Period for target metadata update. 0 disables updates.")
	sizeUpdatePeriod     = flag.Duration("size_update_period", 0, "Period for updating the target size in metadata. 0 disables updates.")
	allowRemoveTarget    = flag.Bool("allow_remove_target", true, "Allow dynamically remove targets based on connection.")
)

func periodic(period time.Duration, fn func()) {
	if period == 0 {
		return
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for range t.C {
		fn()
	}
}

// Under normal conditions, this function will not terminate.  Cancelling
// the context will stop the collector.
func runCollector(ctx context.Context) error {
	if *configFile == "" {
		return errors.New("config_file must be specified")
	}
	if *certFile == "" {
		return errors.New("cert_file must be specified")
	}
	if *keyFile == "" {
		return errors.New("key_file must be specified")
	}

	c := collector{config: &targetpb.Configuration{}, cancelFuncs: map[string]func(){}}
	// Initialize configuration.
	buf, err := ioutil.ReadFile(*configFile)
	if err != nil {
		return fmt.Errorf("Could not read configuration from %q: %v", *configFile, err)
	}
	if err := proto.UnmarshalText(string(buf), c.config); err != nil {
		return fmt.Errorf("Could not parse configuration from %q: %v", *configFile, err)
	}
	if err := target.Validate(c.config); err != nil {
		return fmt.Errorf("Configuration in %q is invalid: %v", *configFile, err)
	}

	// Initialize TLS credentials.
	creds, err := credentials.NewServerTLSFromFile(*certFile, *keyFile)
	if err != nil {
		return fmt.Errorf("Failed to generate credentials %v", err)
	}

	// Initialize cache.
	c.cache = cache.New(nil)

	// Start functions to periodically update metadata stored in the cache for each target.
	go periodic(*metadataUpdatePeriod, c.cache.UpdateMetadata)
	go periodic(*sizeUpdatePeriod, c.cache.UpdateSize)

	// Initialize collectors.
	c.start(context.Background())

	// add targets from tunnel config for testing.
	err = c.addTargetFromTunnelConfig(context.Background())
	if err != nil {
		return fmt.Errorf("failed to add targets from tunnel: %v", err)
	}

	// Create a grpc Server.
	srv := grpc.NewServer(grpc.Creds(creds))
	// Initialize the Collector server.
	cpb.RegisterCollectorServer(srv, coll.New(c.reconnect))
	// Initialize gNMI Proxy Subscribe server.
	subscribeSrv, err := subscribe.NewServer(c.cache)
	if err != nil {
		return fmt.Errorf("Could not instantiate gNMI server: %v", err)
	}
	gnmipb.RegisterGNMIServer(srv, subscribeSrv)
	// Forward streaming updates to clients.
	c.cache.SetClient(subscribeSrv.Update)
	// Register listening port and start serving.
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	<-ctx.Done()
	return ctx.Err()
}

// Container for some of the target state data. It is created once
// for every device and used as a closure parameter by ProtoHandler.
type state struct {
	name   string
	target *cache.Target
	// connected status is set to true when the first gnmi notification is received.
	// it gets reset to false when disconnect call back of ReconnectClient is called.
	connected bool
}

func (s *state) disconnect() {
	s.connected = false
	s.target.Reset()
}

// handleUpdate parses a protobuf message received from the target. This implementation handles only
// gNMI SubscribeResponse messages. When the message is an Update, the GnmiUpdate method of the
// cache.Target is called to generate an update. If the message is a sync_response, then target is
// marked as synchronised.
func (s *state) handleUpdate(msg proto.Message) error {
	if !s.connected {
		s.target.Connect()
		s.connected = true
	}
	resp, ok := msg.(*gnmipb.SubscribeResponse)
	if !ok {
		return fmt.Errorf("failed to type assert message %#v", msg)
	}
	switch v := resp.Response.(type) {
	case *gnmipb.SubscribeResponse_Update:
		// Gracefully handle gNMI implementations that do not set Prefix.Target in their
		// SubscribeResponse Updates.
		if v.Update.GetPrefix() == nil {
			v.Update.Prefix = &gnmipb.Path{}
		}
		if v.Update.Prefix.Target == "" {
			v.Update.Prefix.Target = s.name
		}
		s.target.GnmiUpdate(v.Update)
		log.Infoln("message received, and gnmi updated")
	case *gnmipb.SubscribeResponse_SyncResponse:
		s.target.Sync()
	case *gnmipb.SubscribeResponse_Error:
		return fmt.Errorf("error in response: %s", v)
	default:
		return fmt.Errorf("unknown response %T: %s", v, v)
	}
	return nil
}

type collector struct {
	cache       *cache.Cache
	config      *targetpb.Configuration
	mu          sync.Mutex
	cancelFuncs map[string]func()
}

func (c *collector) addCancel(target string, cancel func()) {
	c.mu.Lock()
	c.cancelFuncs[target] = cancel
	c.mu.Unlock()
}

func (c *collector) reconnect(target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cancel, ok := c.cancelFuncs[target]
	if !ok {
		return fmt.Errorf("no such target: %q", target)
	}
	cancel()
	delete(c.cancelFuncs, target)
	return nil
}

func (c *collector) removeTarget(target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.config.Target[target]; !ok {
		log.Infof("trying to remove target %s, but not found in config. do nothing", target)
		return nil
	}
	delete(c.config.Target, target)

	cancel, ok := c.cancelFuncs[target]
	if !ok {
		return fmt.Errorf("cannot find cancel for target %q", target)
	}
	cancel()
	delete(c.cancelFuncs, target)

	log.Infof("target %s removed", target)
	return nil
}

func (c *collector) addTarget(ctx context.Context, name string, target *targetpb.Target) error {
	if _, ok := c.config.Target[name]; ok {
		log.Infof("trying to add target %s, but already in config. do nothing", target)
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var targetMap map[string]*targetpb.Target
	if c.config.Target != nil {
		targetMap = c.config.Target
	} else {
		targetMap = make(map[string]*targetpb.Target)
	}

	targetMap[name] = target
	c.config.Target = targetMap
	return nil
}

func (c *collector) addTargetFromTunnelConfig(ctx context.Context) error {
	var config targetpb.Configuration
	buf, err := ioutil.ReadFile(*tunnelConfigFile)
	if err != nil {
		return fmt.Errorf("Could not read tunnel configuration from %q: %v", *tunnelConfigFile, err)
	}

	if err := proto.UnmarshalText(string(buf), &config); err != nil {
		return fmt.Errorf("Could not parse configuration from %q: %v", *tunnelConfigFile, err)
	}
	if err := target.Validate(&config); err != nil {
		return fmt.Errorf("Configuration in %q is invalid: %v", *tunnelConfigFile, err)
	}

	for name, target := range config.Target {
		c.addTarget(ctx, name, target)
		c.runSingleTarget(ctx, name)
	}
	return nil
}

func (c *collector) runSingleTarget(ctx context.Context, name string) {
	target, ok := c.config.Target[name]
	if !ok {
		log.Errorf("Unknown target %q", name)
		return
	}

	go func(name string, target *targetpb.Target) {
		s := &state{name: name, target: c.cache.Add(name)}
		qr := c.config.Request[target.Request]
		q, err := client.NewQuery(qr)
		if err != nil {
			log.Errorf("NewQuery(%s): %v", qr.String(), err)
			return
		}
		q.Addrs = target.Addresses

		if target.Credentials != nil {
			q.Credentials = &client.Credentials{
				Username: target.Credentials.Username,
				Password: target.Credentials.Password,
			}
		}

		// TLS is always enabled for a target.
		q.TLS = &tls.Config{
			// Today, we assume that we should not verify the certificate from the target.
			InsecureSkipVerify: true,
		}

		q.Target = name
		q.Timeout = *dialTimeout
		q.ProtoHandler = s.handleUpdate
		if err := q.Validate(); err != nil {
			log.Errorf("query.Validate(): %v", err)
			return
		}
		if *allowRemoveTarget {
			select {
			case <-ctx.Done():
				return
			default:
			}
			sctx, cancel := context.WithCancel(ctx)
			c.addCancel(name, cancel)
			cl := client.BaseClient{}
			if err := cl.Subscribe(sctx, q, gnmiclient.Type); err != nil {
				log.Errorf("Subscribe failed for target %q: %v", name, err)
				// remove target once it becomes unavailable
				c.removeTarget(name)
			}
		} else {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				sctx, cancel := context.WithCancel(ctx)
				c.addCancel(name, cancel)
				cl := client.Reconnect(&client.BaseClient{}, s.disconnect, nil)
				if err := cl.Subscribe(sctx, q, gnmiclient.Type); err != nil {
					log.Errorf("Subscribe failed for target %q: %v", name, err)
				}
			}
		}
	}(name, target)
}

func (c *collector) start(ctx context.Context) {
	for name := range c.config.Target {
		c.runSingleTarget(ctx, name)
	}
}

func main() {
	// Flag initialization.
	flag.Parse()
	log.Exit(runCollector(context.Background()))
}
