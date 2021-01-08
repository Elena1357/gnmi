#!/bin/bash

go run ./gnmi_collector.go --config_file=docker/config/example.cfg  --tunnel_config_file=docker/config/tunnel_targets.cfg --key_file=../../testing/fake/gnmi/cmd/fake_server/fakekey.key  --cert_file=../../testing/fake/gnmi/cmd/fake_server/fakecrt.crt --port=1234 --logtostderr
