#!/bin/bash
go run ./server.go --config config.pb.txt --text --port 55321 --server_crt fakecrt.crt --server_key fakekey.key --allow_no_client_auth --logtostderr