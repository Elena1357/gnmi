#!/bin/bash
go run ./server.go --config=config.pb.txt \
--server_key=/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.key \
--server_crt=/usr/local/google/home/jxxu/Documents/projects/gnmi_dialout/test/github.com/openconfig/grpctunnel/example/localhost.crt \
--port=54322 --tunnel_addr=localhost:4321 --allow_no_client_auth --text