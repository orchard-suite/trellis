.DEFAULT_GOAL := mac
test:
	go test ./...
mac:
	$(MAKE) build
	open -a App
build:
	xcodebuild build
