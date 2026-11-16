// rachio asks Rachio's cloud API about an account's irrigation controllers
// and drives them; the command is internal/cli.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/mycophonic/primordium/app"
	"github.com/mycophonic/primordium/app/shutdown"
	"github.com/mycophonic/primordium/network"

	"github.com/duncanite/go-rachio/internal/cli"
)

// requestTimeout bounds one request to Rachio's servers.
const requestTimeout = 30 * time.Second

func main() {
	app.Main(&app.Options{Name: "rachio"}, func(ctx context.Context) error {
		client := &http.Client{Transport: network.NewTransport(), Timeout: requestTimeout}

		if code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, client, os.Getenv); code != cli.ExitOK {
			return shutdown.ExitError(code)
		}

		return nil
	})
}
