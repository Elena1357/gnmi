#!/bin/bash

# go run ./gnmi_collector.go --config_file=docker/config/example.cfg --key_file=docker/config/key.pem  --cert_file=docker/config/cert.pem --port=1234 --logtostderr
go run ./gnmi_collector.go --config_file=docker/config/example.cfg --port=1234 --logtostderr \
--key_file=/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.key \
--cert_file=/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.crt \
-alsologtostderr --stderrthreshold=INFO -v=1