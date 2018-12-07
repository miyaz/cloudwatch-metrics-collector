# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
DEPCMD=dep
DEPENSURE=$(DEPCMD) ensure
GOBINDATA=go-bindata
BINARY_NAME=cmc
BINARY_UNIX=$(BINARY_NAME)_unix
BINDATA_FILE=bindata.go

all: deps bindata build
#all: deps bindata test build
build: build-macos build-linux
bindata:
	$(GOBINDATA) data/
test: 
	$(GOTEST) -v ./...
clean: 
	$(GOCLEAN)
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_UNIX)
	rm -f $(BINDATA_FILE)
run: bindata
	$(GOBUILD) -o $(BINARY_NAME) -v ./...
	./$(BINARY_NAME)
deps:
	$(GOGET) -v github.com/jteeuwen/go-bindata/...
	$(DEPENSURE)
build-macos: bindata
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(BINARY_NAME) -v bindata.go main.go
build-linux: bindata
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BINARY_UNIX) -v bindata.go main.go
