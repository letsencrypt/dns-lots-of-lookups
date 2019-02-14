// Package dnslol provides the ability to run many queries against multiple
// DNS servers.
package dnslol

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/miekg/dns"
	prom "github.com/prometheus/client_golang/prometheus"
)

const (
	maxInsertRetries = 3
)

type server struct {
	id      int64
	address string
}

// an Experiment holds settings related to the lookups that will be performed
// when the Experiment is started with the `Start` function.
type Experiment struct {
	// The HTTP bind address for the Prometheus metrics server.
	MetricsAddr string
	// The command line that was used to construct the Experiment (e.g. the
	// arguments passed to the `dnslol` command).
	CommandLine string
	// One or more DNS server addresses with port numbers
	Servers []string
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
	// Whether or not to print lookup results to stdout.
	PrintResults bool
	// How many times to repeat the same query against each server
	Count int

	// A DB connection for storing results.
	db *sql.DB
	// The ID assigned by the DB for the Experiment row.
	id int64
	// The servers that the Experiment will query.
	servers []server
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
	if len(e.Servers) < 1 {
		return errors.New("Experiment must have at least one Servers address")
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
	if !e.CheckA && !e.CheckAAAA && !e.CheckTXT {
		return errors.New(
			"Experiment must have at least one CheckA, CheckAAAA, or CheckTXT " +
				"set to true")
	}
	if e.Count < 1 {
		return errors.New("Experiment must have a Count greater than 0")
	}
	return nil
}

// A query is a struct holding a name to query for, a type to request, and the
// address of the DNS server to be queried.
type query struct {
	Server server
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
// and directed to the Experiment's DNS Servers the. Each query performed by
// runQueries will increment the "attempts" stat for the servers queried.
// A "result" stat will be incremented based on the result of the query for the
// servers queried. Successful queries will increment the "successes" stat for
// the servers queried. If the Experiment has a true value for PrintResults each
// query result will be printed to standard out.
func (e Experiment) runQueries(dnsClient *dns.Client, name string) error {
	if dnsClient == nil {
		return errors.New("runQueries requires a non-nil dnsClient instance")
	}

	// Build the queries for this name for each of the nameservers
	queries := e.buildQueries(name)

	// Randomize the queries so we don't consistently query one of the nameservers
	// first, which could introduce a slight bias.
	rand.Shuffle(len(queries), func(i, j int) {
		queries[i], queries[j] = queries[j], queries[i]
	})
	var wg sync.WaitGroup
	// Run the built queries, populating the prometheus result stat according to
	// the results
	for _, q := range queries {
		wg.Add(1)
		// Run the queries on a goroutine so slowness in one server doesn't impact
		// the submission rate to the other server.
		go func(q query) {
			stats.attempts.With(prom.Labels{"server": q.Server.address}).Add(1)
			resultLabels := prom.Labels{"server": q.Server.address}
			err := e.queryOne(dnsClient, q)
			// If the result was an error, put the error string in the result label
			if err != nil {
				resultLabels["result"] = err.Error()
			} else {
				// If the result was successful, increment the success stat and put
				// "ok" in the result label
				stats.successes.With(prom.Labels{"server": q.Server.address}).Add(1)
				resultLabels["result"] = "ok"
			}
			if e.PrintResults {
				printQueryResult(q, err)
			}
			stats.results.With(resultLabels).Add(1)
			e.saveQueryResult(q, err)
			wg.Done()
		}(q)
	}
	wg.Wait()
	return nil
}

func printQueryResult(q query, err error) {
	var line strings.Builder
	fmt.Fprintf(&line, "Server=%s Name=%s QueryType=%s",
		q.Server.address, q.Name, dns.TypeToString[q.Type])
	if err != nil {
		fmt.Fprintf(&line, " Error=%s Outcome=bad", err.Error())
	} else {
		fmt.Fprintf(&line, " Outcome=ok")
	}
	log.Printf("%s", line.String())
}

func (e Experiment) saveQueryResult(q query, err error) {
	var errBlob []byte
	if err != nil {
		errBlob = []byte(err.Error())
	}

	for i := 0; i < maxInsertRetries; i++ {
		_, err = e.db.Exec(
			"INSERT INTO results (`name`, `type`, `error`, `serverID`, `experimentID`) VALUES (?, ?, ?, ?, ?);",
			q.Name, q.Type, errBlob, q.Server.id, e.id)
		if err == nil {
			break
		}
	}
	if err != nil {
		log.Fatalf(
			"Failed to insert result for %q query to %q after %d tries: %v\n",
			q.Name, q.Server.address, maxInsertRetries, err)
	}
}

// buildQueries creates queries for the given name, e.Count per server. The types of
// queries that are built depends on the Experiment's CheckA, CheckAAAA,
// and CheckTXT settings.
func (e Experiment) buildQueries(name string) []query {
	// queryPerServer returns a list with one query per server for the given name
	// and type.
	queryPerServer := func(name string, typ uint16) []query {
		var results []query
		for _, server := range e.servers {
			for i := 0; i < e.Count; i++ {
				results = append(results, query{
					Name:   name,
					Type:   typ,
					Server: server,
				})
			}
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

	return queries
}

// queryOne performs one single query using the given dnsClient. For successful
// queries (e.g. resulting in a RcodeSuccess) nil is returned. Queries that
// result in an error, or an Rcode other than RcodeSuccess return an error. In
// all cases the queryTimes latency stat is updated for the server and query
// type performed.
func (e Experiment) queryOne(dnsClient *dns.Client, q query) error {
	// Build a DNS msg based on the query details
	typStr := dns.TypeToString[q.Type]
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(q.Name), q.Type)

	// Query the server and record the time taken
	in, rtt, err := dnsClient.Exchange(m, q.Server.address)
	stats.queryTimes.With(prom.Labels{
		"server": q.Server.address,
		"type":   typStr}).Observe(rtt.Seconds())
	if err != nil {
		if ne, ok := err.(*net.OpError); ok && ne.Timeout() {
			return fmt.Errorf("timeout")
		} else if _, ok := err.(*net.OpError); ok {
			return fmt.Errorf("net err")
		}
		return err
	} else if in.Rcode != dns.RcodeSuccess {
		// If the rcode wasn't a successful rcode, return an error with the rCode as
		// the string
		rcodeStr := dns.RcodeToString[in.Rcode]
		return errors.New(rcodeStr)
	}
	// Otherwise everything went well! Return nil
	return nil
}

func (e *Experiment) saveExperiment() error {
	if e.db == nil {
		return errors.New("saveExperiment requires a non-nil db")
	}

	// Create the experiment in the DB
	result, err := e.db.Exec(
		`INSERT INTO experiments (start, commandline) VALUES (?, ?);`,
		time.Now(),
		e.CommandLine)
	if err != nil {
		return err
	}
	e.id, err = result.LastInsertId()
	if err != nil {
		return err
	}

	// Then create the associated servers
	savedServers := make([]server, len(e.Servers))
	for i, srvAddr := range e.Servers {
		result, err = e.db.Exec(
			`INSERT INTO servers (address, experimentID) VALUES (?, ?);`,
			srvAddr, e.id)
		if err != nil {
			return err
		}
		srvID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		savedServers[i] = server{
			id:      srvID,
			address: srvAddr,
		}
	}
	e.servers = savedServers
	return nil
}

// End updates the Experiment's end date and closes the Experiment's database
// connection or return an error.
func (e Experiment) Close() error {
	if e.db == nil {
		return errors.New("Close requires a non-nil db")
	}
	if e.id == 0 {
		return errors.New("Experiment does not have an ID")
	}

	// Update the experiment in the DB
	result, err := e.db.Exec(
		`UPDATE experiments SET end=? WHERE id=?;`,
		time.Now(),
		e.id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf(
			"Expected to update one experiment row, actually updated %d", updated)
	}

	return e.db.Close()
}

// Start will run the given Experiment by initializing and running a metrics
// server and then spawning goroutines to process queries according to the
// Experiment parameters. The spawned goroutines will read names to query from
// the provided names channel. When a query work item for a name is completed
// the spawned worker goroutines will call the provided WaitGroup's Done
// function. An error is returned from Start if the given Experiment is not
// valid.
func Start(e *Experiment, names <-chan string, wg *sync.WaitGroup, dsn string, maxConns int) error {
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

	// Connect to the database
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(maxConns)
	e.db = db

	// Store the experiment to get an ID and to populate the `servers` slice with
	// IDs
	err = e.saveExperiment()
	if err != nil {
		log.Fatalf("error saving experiment to db: %v\n", err)
	}

	dnsClient := &dns.Client{
		Net:         e.Proto,
		ReadTimeout: e.Timeout,
	}

	// Spawn worker goroutines for the experiment
	go spawn(*e, dnsClient, names, wg)

	return nil
}
