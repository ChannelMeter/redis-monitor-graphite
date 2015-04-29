#!/bin/bash
GOOS=linux CGO_ENABLED=0 go build -a -installsuffix cgo -o ./rmg ./rmg.go
