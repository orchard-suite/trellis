# A monorepo's Makefile: a default that runs another target, a variable,
# a pattern rule, special targets, and a continued prerequisite list.
.PHONY: default ios android test web
VERSION := $(shell cat VERSION)
BIN = bin/app

default: ios

ios:
	@cd ios && bash commands/install.command

android: gradle \
	sdk
	./gradlew installDebug

test: ; go test ./...

web:
	$(MAKE) -C web build VERSION=$(VERSION)

%.o: %.c
	cc -c $<

$(BIN): main.go
	go build -o $@
