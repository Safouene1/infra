package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/infrahq/infra/api"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	client := api.Client{
		Name:      "testing",
		Version:   "9.9.90",
		URL:       os.Getenv("INFRA_SERVER"),
		AccessKey: os.Getenv("INFRA_ACCESS_KEY"),
		HTTP: http.Client{
			Transport: transport,
		},
	}

	n, _ := strconv.ParseInt(os.Getenv("NUM_REQUESTS"), 10, 64)
	index, _ := strconv.ParseInt(os.Getenv("INDEX"), 10, 64)

	g, ctx := errgroup.WithContext(context.Background())
	for i := 0; i < int(n); i++ {
		i := i
		g.Go(func() error {
			fmt.Printf("starting request %d\n", i+1)
			resp, err := client.ListGrants(ctx, api.ListGrantsRequest{
				Destination:     "none",
				BlockingRequest: api.BlockingRequest{LastUpdateIndex: index},
			})
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				fmt.Printf("error from request %d: %v\n", i+1, err)
				return err
			}
			fmt.Printf("request %d next index: %v\n", i+1, resp.LastUpdateIndex.Index)
			return nil
		})
		time.Sleep(200 * time.Millisecond)
	}

	return g.Wait()
}
