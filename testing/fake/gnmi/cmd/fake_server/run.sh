#!/bin/bash
./fake_server --config config.pb.txt --text --port 12345 --server_crt fakecrt.crt --server_key fakekey.key --allow_no_client_auth --logtostderr
# ./fake_server --config example.cfg --text --port 12345 --server_crt fakecrt.crt --server_key fakekey.key --allow_no_client_auth --logtostderr