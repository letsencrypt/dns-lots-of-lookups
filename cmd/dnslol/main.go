// The dnslol command is a command line utility for doing lots of DNS lookups.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	_ "net/http/pprof"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/letsencrypt/dns-lots-of-lookups/dnslol"
)

var (
	metricsAddrFlag = flag.String(
		"metricsAddr",
		":6363",
		"Bind address for HTTP metrics server")
	dbConnFlag = flag.String(
		"db",
		"dnslol:dnslol@tcp(10.10.10.2:3306)/dnslol-results",
		"Database connection URL")
	dbMaxConnsFlag = flag.Int(
		"dbMaxConns",
		250)
	serversFlag = flag.String(
		"servers",
		"127.0.0.1:53",
		"Comma-separated list of DNS servers")
	timeoutFlag = flag.Duration(
		"timeout",
		30*time.Second,
		"DNS query timeout duration")
	protoFlag = flag.String(
		"proto",
		"udp",
		"DNS protocol (tcp or udp)")
	reverseNamesFlag = flag.Bool(
		"reverse",
		false,
		"Label-wise reverse input domains (e.g `com.google.www` -> `www.google.com`)")
	parallelFlag = flag.Int(
		"parallel",
		5,
		"Number of parallel queries to perform")
	spawnRateFlag = flag.Int(
		"spawnRate",
		100,
		"Number of goroutines to spawn per spawnInterval")
	spawnIntervalFlag = flag.Duration(
		"spawnInterval",
		1*time.Minute,
		"Interval on which to spawn goroutines")
	checkAFlag = flag.Bool(
		"checkA",
		true,
		"Lookup A records")
	checkAAAAFlag = flag.Bool(
		"checkAAAA",
		false,
		"Whether to check AAAA records")
	checkTXTFlag = flag.Bool(
		"checkTXT",
		false,
		"Whether to check TXT records")
	printResultsFlag = flag.Bool(
		"print",
		true,
		"Print lookup results to stdout")
	countFlag = flag.Int(
		"count",
		1,
		"How many times to repeat the same query against each server")
)

// checkUlimit checks the *parallelFlag value against the system RLIMIT_NOFILE
// value controlling the number of files a process can have open. If the
// *parallelFlag value is larger than the current RLIMIT_NOFILE an error is
// returned. Allowing the experiment to proceed without fixing the ulimit will
// result in running out of file handles.
func checkUlimit() error {
	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
		return err
	}
	if *parallelFlag > int(rLimit.Cur) {
		return fmt.Errorf(
			`current ulimit for "nofile" lower than requested -parallel: %d vs %d"`,
			rLimit.Cur, *parallelFlag)
	}
	return nil
}

// reverseName performs a label-wise reversal of the given domain name. E.g.
// "com.google.www" -> "www.google.com".
func reverseName(domain string) string {
	labels := strings.Split(domain, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, ".")
}

// parseServers splits a raw serversFlag string containing one or more DNS
// server addresses, returning a slice of individual server addresses. If no
// port is specified in the server addresses it is assumed to be port 53 (the
// default DNS port).
func parseServers(raw string) []string {
	servers := strings.Split(raw, ",")
	for i := range servers {
		if !strings.Contains(servers[i], ":") {
			servers[i] = servers[i] + ":53"
		}
	}
	return servers
}

func main() {
	flag.Parse()

	// There's no point using a -parallel higher than ulimits allow
	if err := checkUlimit(); err != nil {
		log.Fatalf("Error: %v\n", err)
	}

	// Split the -servers input and construct a selector to use
	dnsServerAddresses := parseServers(*serversFlag)

	// Construct an Experiment with the command line flag options
	exp := dnslol.Experiment{
		MetricsAddr:   *metricsAddrFlag,
		CommandLine:   strings.Join(os.Args, " "),
		Servers:       dnsServerAddresses,
		Proto:         *protoFlag,
		Timeout:       *timeoutFlag,
		Parallel:      *parallelFlag,
		SpawnRate:     *spawnRateFlag,
		SpawnInterval: *spawnIntervalFlag,
		CheckA:        *checkAFlag,
		CheckAAAA:     *checkAAAAFlag,
		CheckTXT:      *checkTXTFlag,
		PrintResults:  *printResultsFlag,
		Count:         *countFlag,
	}

	// Read domain names from standard in
	//
	// TODO(@cpu): It would be better to stream stdin into the names channel so we
	// don't have to consume the entire stdin input into memory at startup.
	stdinBytes, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("Error reading names from standard in: %v\n", err)
	}

	// Create a channel for feeding domain names to the experiment
	names := make(chan string)
	// Create a waitgroup so we can tell when all domain names have been processed
	wg := sync.WaitGroup{}

	// Start the experiment - it will initially be blocked waiting for domain
	// names
	err = dnslol.Start(&exp, names, &wg, *dbConnFlag, *dbMaxConnsFlag)
	if err != nil {
		log.Fatalf("Error running experiment: %v\n", err)
	}
	// Close the experiment's database connection when everything is finished.
	defer func() {
		err := exp.Close()
		if err != nil {
			log.Fatalf("Error closing experiment: %v\n", err)
		}
	}()

	// Feed each of the domain names from stdin to the experiment for processing
	for _, name := range strings.Split(string(stdinBytes), "\n") {
		if name == "" {
			continue
		}
		wg.Add(1)
		if *reverseNamesFlag {
			name = reverseName(name)
		}
		names <- name
	}

	// Close the names channel and wait for the experiment to be finished
	close(names)
	wg.Wait()
}
