// cashsdk is the CashSDK command-line interface: in-app purchases,
// subscriptions and paywalls for iOS and Android, from your terminal.
package main

import (
	"os"

	"github.com/cashsdk/cashsdk-cli/internal/cmd"
)

// version is stamped by the release build:
//
//	go build -ldflags "-s -w -X main.version=2.0.2"
var version = "dev"

func main() {
	cmd.Version = version
	os.Exit(cmd.Dispatch(os.Args[1:]))
}
