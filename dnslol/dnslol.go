// Package dnslol provides the ability to run many queries against multiple
// DNS servers.
package dnslol

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	prom "github.com/prometheus/client_golang/prometheus"
)

// an Experiment holds settings related to the lookups that will be performed
// when the Experiment is started with the `Start` function.
type Experiment struct {
	// The HTTP bind address for the Prometheus metrics server.
	MetricsAddr string
	// The command line that was used to construct the Experiment (e.g. the
	// arguments passed to the `dnslol` command).
	CommandLine string
	// A DNSServerSelector for choosing server addresses for doing lookups.
	Selector DNSServerSelector
	// The protocol used to talk to selected DNS Servers ("tcp" or "udp").
	Proto string
	// A Duration after which DNS queries are considered to have timed out.
	Timeout time.Duration
	// The number of queries to perform in parallel.
	Parallel int
	// The rate at which to spawn new query goroutines.
	SpawnRate int
	// A Duration to sleep between starting SpawnRate new queries.
	SpawnInterval time.Duration
	// Whether or not to do queries for A records.
	CheckA bool
	// Whether or not to do queries for AAA records.
	CheckAAAA bool
	// Whether or not to do queries for `_acme-challenge.` TXT records.
	CheckTXT bool
	// Whether or not to do multiple CAA queries (e.g. w/ tree-climbing) for
	// domains.
	CheckCAA bool
	// Whether or not to print lookup results to stdout.
	PrintResults bool
}

// Valid checks whether a given Experiment is valid. It returns an error if the
// Experiment has problems (missing mandatory fields, no query types selected,
// etc).
func (e Experiment) Valid() error {
	if e.MetricsAddr == "" {
		return errors.New("Experiment must have a non-empty MetricsAddr")
	}
	if e.CommandLine == "" {
		return errors.New("Experiment must have a non-empty CommandLine")
	}
	if e.Selector == nil {
		return errors.New("Experiment must have a non-nil Selector")
	}
	if e.Proto != "tcp" && e.Proto != "udp" {
		return errors.New(`Experiment must have a Proto value of "tcp" or "udp"`)
	}
	if e.Timeout.Seconds() < 1 {
		return errors.New("Experiment must have a Timeout greater than 1 second")
	}
	if e.Parallel < 1 {
		return errors.New("Experiment must have a Parallel value greater than 1")
	}
	if e.SpawnRate < 1 {
		return errors.New("Experiment must have a SpawnRate value greater than 1")
	}
	if !e.CheckA && !e.CheckAAAA && !e.CheckTXT && !e.CheckCAA {
		return errors.New(
			"Experiment must have at least one CheckA, CheckAAAA, CheckTXT or " +
				"CheckCAA set to true")
	}
	return nil
}

// A query is a struct holding a name to query for, a type to request, and the
// address of the DNS server to be queried.
type query struct {
	// Hostname/port of a DNS server to query
	Server string
	// The name to query the server for
	Name string
	// The DNS record type to ask the server for
	Type uint16
}

// spawn will create worker goroutines up to the Experiment's configured
// Parallel setting. It creates new goroutines in batches based on the
// Experiment's SpawnRate. After starting a new batch spawn will sleep for the
// Experiment's configured SpawnInterval. Worker goroutines will call runQueries
// for each name. Once the queries for a given name are completed the provided
// waitgroup's Done function is called. If there is an error running queries
// (not an error result from a query) log.Fatal is called to terminate the
// experiment.
func spawn(exp Experiment, dnsClient *dns.Client, names <-chan string, wg *sync.WaitGroup) {
	for i := 0; i < exp.Parallel; {
		for j := 0; j < exp.SpawnRate; i, j = i+1, j+1 {
			go func() {
				for name := range names {
					err := exp.runQueries(dnsClient, name)
					if err != nil {
						log.Fatalf("Error running queries for %q: %v\n", name, err)
					}
					wg.Done()
				}
			}()
		}
		time.Sleep(exp.SpawnInterval)
	}
}

// runQueries will build & execute queries for the given name based on the
// Experiment's settings. The queries will be made with the provided dnsClient
// and directed to DNS servers based on the Experiment's DNSServerSelector. An
// error is returned if there is a problem selecting a server or if the provided
// dnsClient is nil. Each query performed by runQueries  will increment the
// "attempts" stat for the servers queried. A "result" stat will be incremented
// based on the result of the query for the servers queried. Successful queries
// will increment the "successes" stat for the servers queried. If the
// Experiment has a true value for PrintResults each query result will be
// printed to standard out.
func (e Experiment) runQueries(dnsClient *dns.Client, name string) error {
	if dnsClient == nil {
		return errors.New("runQueries requires a non-nil dnsClient instance")
	}

	// Pick some servers based on the Experiment's Selector. It is expected to
	// return one or more DNS server hostnames.
	servers := e.Selector.PickServers()
	if len(servers) == 0 {
		// This shouldn't ever happen, but be defensive in case of a bug :-)
		return errors.New(
			"Experiment selector returned zero DNS server addresses from PickServers")
	}

	// Build the queries for this name for each of the nameservers
	queries := e.buildQueries(name, servers)

	// Run the built queries, populating the prometheus result stat according to
	// the results
	for _, q := range queries {
		stats.attempts.With(prom.Labels{"server": q.Server}).Add(1)
		resultLabels := prom.Labels{"server": q.Server}
		resultType, success, err := e.queryOne(dnsClient, q)
		// If the result was an error, put the error string in the result label
		if err != nil {
			resultLabels["result"] = err.Error()
		} else if success {
			// If the result was successful, increment the success stat and put the
			// resultType in the result label
			stats.successes.With(prom.Labels{"server": q.Server}).Add(1)
			resultLabels["result"] = resultType
		} else {
			// Otherwise, if the result was not an error but wasn't a success just put
			// the resultType in the resultLabels. Don't increment the successes stat.
			resultLabels["result"] = resultType
		}
		// TODO(@cpu): This should be a separate function.
		if e.PrintResults {
			outcome := "bad"
			if success {
				outcome = "ok"
			}
			var line strings.Builder
			fmt.Fprintf(&line, "Server=%s Name=%s QueryType=%s Result=%s",
				q.Server, q.Name, dns.TypeToString[q.Type], resultType)
			if err != nil {
				fmt.Fprintf(&line, " Error=%s", err.Error())
			}
			fmt.Fprintf(&line, " Outcome=%s", outcome)
			log.Printf("%s", line.String())
		}
		stats.results.With(resultLabels).Add(1)
	}
	return nil
}

// buildQueries creates queries for the given name, one per server. The types of
// queries that are built depends on the Experiment's CheckA, CheckAAAA,
// CheckTXT and CheckCAA settings.
func (e Experiment) buildQueries(name string, servers []string) []query {
	// queryPerServer returns a list with one query per server for the given name
	// and type.
	queryPerServer := func(name string, typ uint16) []query {
		var results []query
		for _, server := range servers {
			results = append(results, query{
				Name:   name,
				Type:   typ,
				Server: server,
			})
		}
		return results
	}

	var queries []query
	if e.CheckA {
		queries = append(queries,
			queryPerServer(name, dns.TypeA)...)
	}
	if e.CheckAAAA {
		queries = append(queries,
			queryPerServer(name, dns.TypeAAAA)...)
	}
	if e.CheckTXT {
		queries = append(queries,
			queryPerServer(name, dns.TypeTXT)...)
	}
	if e.CheckCAA {
		// We check CAA differently from other domains by splitting the individual
		// domain labels up and mimicking CAA tree climbing by making a CAA query
		// for each label for each server.
		labels := strings.Split(name, ".")
		for i := 0; i < len(labels); i++ {
			queries = append(queries,
				queryPerServer(strings.Join(labels[i:], "."), dns.TypeCAA)...)
		}
	}

	return queries
}

// queryOne performs one single query using the given dnsClient. For successful
// queries (e.g. resulting in a RcodeSuccess) return that rcode string, a true
// bool and a nil error. Unsuccessful queries either return an rcode string
// other than RcodeSuccess, a false bool and a nil error or an empty rcode
// string, a false bool and a non-nil error. In all cases the queryTimes latency
// stat is updated for the server and query type performed.
func (e Experiment) queryOne(dnsClient *dns.Client, q query) (string, bool, error) {
	// Build a DNS msg based on the query details
	typStr := dns.TypeToString[q.Type]
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(q.Name), q.Type)

	// Query the server and record the time taken
	in, rtt, err := dnsClient.Exchange(m, q.Server)
	stats.queryTimes.With(prom.Labels{
		"server": q.Server,
		"type":   typStr}).Observe(rtt.Seconds())
	if err != nil {
		if ne, ok := err.(*net.OpError); ok && ne.Timeout() {
			err = fmt.Errorf("timeout")
		} else if _, ok := err.(*net.OpError); ok {
			err = fmt.Errorf("net err")
		}
		// If there was an error, it was a failure that didn't produce an rcodeStr.
		// Return an empty rcodeStr, a failure bool, and the error.
		return "", false, fmt.Errorf("for %s: %s", typStr, err)
	} else if in.Rcode != dns.RcodeSuccess {
		rcodeStr := dns.RcodeToString[in.Rcode]
		// If the rcode wasn't a successful rcode, return its str form, a failure
		// bool, and no error.
		return rcodeStr, false, nil
	}
	for _, answer := range in.Answer {
		// We additionally check that CAA records don't have a case mismatch
		//
		// TODO(@cpu): This smells like something that should be behind
		// a flag/Experiment bool.
		if caaR, ok := answer.(*dns.CAA); ok && strings.ToLower(caaR.Tag) != caaR.Tag {
			// If there was a case mismatch return no rcode Str, a failure bool, and
			// a manufactured error.
			return "", false, fmt.Errorf("tag mismatch for %s: %s", strings.ToLower(caaR.Tag), caaR)
		}
	}
	// Otherwise everything went well! Return the rcode str, a true success bool
	// and no error.
	return dns.RcodeToString[in.Rcode], true, nil
}

// Start will run the given Experiment by initializing and running a metrics
// server and then spawning goroutines to process queries according to the
// Experiment parameters. The spawned goroutines will read names to query from
// the provided names channel. When a query work item for a name is completed
// the spawned worker goroutines will call the provided WaitGroup's Done
// function. An error is returned from Start if the given Experiment is not
// valid.
func Start(e Experiment, names <-chan string, wg *sync.WaitGroup) error {
	if err := e.Valid(); err != nil {
		return err
	}

	// Create & start a metrics server
	metricsServer := initMetrics(e.MetricsAddr)
	go func() {
		err := metricsServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("metrics server failed: %v", err)
		}
	}()

	dnsClient := &dns.Client{
		Net:         e.Proto,
		ReadTimeout: e.Timeout,
	}

	// Spawn worker goroutines for the experiment
	go spawn(e, dnsClient, names, wg)

	return nil
}
