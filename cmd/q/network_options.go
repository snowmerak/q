package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
)

type networkCommandOptions struct {
	host    string
	port    int
	hostSet bool
	portSet bool
}

func parseNetworkOptions(command string, args []string, output io.Writer) (networkCommandOptions, error) {
	options := networkCommandOptions{port: -1}
	flags := flag.NewFlagSet("q "+command+" start", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(output, "usage: q %s start [--host <ip>] [--port <port>]\n", command)
		flags.PrintDefaults()
	}
	flags.StringVar(&options.host, "host", "", "override the configured listen IP address")
	flags.IntVar(&options.port, "port", -1, "override the configured listen port (0 selects a random port)")
	if err := flags.Parse(args); err != nil {
		return networkCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return networkCommandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	flags.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "host":
			options.hostSet = true
		case "port":
			options.portSet = true
		}
	})
	if options.hostSet && net.ParseIP(options.host) == nil {
		return networkCommandOptions{}, fmt.Errorf("host %q is not an IP address", options.host)
	}
	if options.portSet && (options.port < 0 || options.port > 65535) {
		return networkCommandOptions{}, errors.New("port must be between 0 and 65535")
	}
	return options, nil
}
