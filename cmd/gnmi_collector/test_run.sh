#!/bin/bash
#

# go run ./gnmi_collector.go --config_file=docker/config/example.cfg --key_file=docker/config/key.pem  --cert_file=docker/config/cert.pem --port=1234 --logtostderr
go run ./gnmi_collector.go --config_file=docker/config/example.cfg --key_file=../../testing/fake/gnmi/cmd/fake_server/fakekey.key  --cert_file=../../testing/fake/gnmi/cmd/fake_server/fakecrt.crt --port=1234 --logtostderr
