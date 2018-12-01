# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: gbgm android ios gbgm-cross swarm evm all test clean
.PHONY: gbgm-linux gbgm-linux-386 gbgm-linux-amd64 gbgm-linux-mips64 gbgm-linux-mips64le
.PHONY: gbgm-linux-arm gbgm-linux-arm-5 gbgm-linux-arm-6 gbgm-linux-arm-7 gbgm-linux-arm64
.PHONY: gbgm-darwin gbgm-darwin-386 gbgm-darwin-amd64
.PHONY: gbgm-windows gbgm-windows-386 gbgm-windows-amd64

GOBIN = $(shell pwd)/build/bin
GO ?= latest

gbgm:
	build/env.sh go run build/ci.go install ./cmd/gbgm
	@echo "Done building."
	@echo "Run \"$(GOBIN)/gbgm\" to launch gbgm."

swarm:
	build/env.sh go run build/ci.go install ./cmd/swarm
	@echo "Done building."
	@echo "Run \"$(GOBIN)/swarm\" to launch swarm."

all:
	build/env.sh go run build/ci.go install

android:
	build/env.sh go run build/ci.go aar --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/gbgm.aar\" to use the library."

ios:
	build/env.sh go run build/ci.go xcode --local
	@echo "Done building."
	@echo "Import \"$(GOBIN)/Gbgm.framework\" to use the library."

test: all
	build/env.sh go run build/ci.go test

clean:
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go get -u golang.org/x/tools/cmd/stringer
	env GOBIN= go get -u github.com/jteeuwen/go-bindata/go-bindata
	env GOBIN= go get -u github.com/fjl/gencodec
	env GOBIN= go install ./cmd/abigen

# Cross Compilation Targets (xgo)

gbgm-cross: gbgm-linux gbgm-darwin gbgm-windows gbgm-android gbgm-ios
	@echo "Full cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-*

gbgm-linux: gbgm-linux-386 gbgm-linux-amd64 gbgm-linux-arm gbgm-linux-mips64 gbgm-linux-mips64le
	@echo "Linux cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-*

gbgm-linux-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/386 -v ./cmd/gbgm
	@echo "Linux 386 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep 386

gbgm-linux-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/amd64 -v ./cmd/gbgm
	@echo "Linux amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep amd64

gbgm-linux-arm: gbgm-linux-arm-5 gbgm-linux-arm-6 gbgm-linux-arm-7 gbgm-linux-arm64
	@echo "Linux ARM cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep arm

gbgm-linux-arm-5:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-5 -v ./cmd/gbgm
	@echo "Linux ARMv5 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep arm-5

gbgm-linux-arm-6:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-6 -v ./cmd/gbgm
	@echo "Linux ARMv6 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep arm-6

gbgm-linux-arm-7:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-7 -v ./cmd/gbgm
	@echo "Linux ARMv7 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep arm-7

gbgm-linux-arm64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm64 -v ./cmd/gbgm
	@echo "Linux ARM64 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep arm64

gbgm-linux-mips:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips --ldflags '-extldflags "-static"' -v ./cmd/gbgm
	@echo "Linux MIPS cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep mips

gbgm-linux-mipsle:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mipsle --ldflags '-extldflags "-static"' -v ./cmd/gbgm
	@echo "Linux MIPSle cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep mipsle

gbgm-linux-mips64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64 --ldflags '-extldflags "-static"' -v ./cmd/gbgm
	@echo "Linux MIPS64 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep mips64

gbgm-linux-mips64le:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64le --ldflags '-extldflags "-static"' -v ./cmd/gbgm
	@echo "Linux MIPS64le cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-linux-* | grep mips64le

gbgm-darwin: gbgm-darwin-386 gbgm-darwin-amd64
	@echo "Darwin cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-darwin-*

gbgm-darwin-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/386 -v ./cmd/gbgm
	@echo "Darwin 386 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-darwin-* | grep 386

gbgm-darwin-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/amd64 -v ./cmd/gbgm
	@echo "Darwin amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-darwin-* | grep amd64

gbgm-windows: gbgm-windows-386 gbgm-windows-amd64
	@echo "Windows cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-windows-*

gbgm-windows-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/386 -v ./cmd/gbgm
	@echo "Windows 386 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-windows-* | grep 386

gbgm-windows-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/amd64 -v ./cmd/gbgm
	@echo "Windows amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gbgm-windows-* | grep amd64
