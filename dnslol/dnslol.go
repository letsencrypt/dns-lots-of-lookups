// Package dnslol provides the ability to run many queries against configured
// DNS recursive resolvers.
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

type Experiment struct {
	MetricsAddr   string
	CommandLine   string
	Selector      DNSServerSelector
	Proto         string
	Timeout       time.Duration
	Parallel      int
	SpawnRate     int
	SpawnInterval time.Duration
	CheckA        bool
	CheckAAAA     bool
	CheckTXT      bool
	CheckCAA      bool
}

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

func (e Experiment) runQueries(dnsClient *dns.Client, name string) error {
	if dnsClient == nil {
		return errors.New("runQueries requires a non-nil dnsClient instance")
	}
	type query struct {
		Name string
		Type uint16
	}
	var queries []query
	if e.CheckA {
		queries = append(queries, query{
			Name: name,
			Type: dns.TypeA,
		})
	}
	if e.CheckAAAA {
		queries = append(queries, query{
			Name: name,
			Type: dns.TypeAAAA,
		})
	}
	if e.CheckTXT {
		queries = append(queries, query{
			Name: name,
			Type: dns.TypeTXT,
		})
	}
	if e.CheckCAA {
		labels := strings.Split(name, ".")
		for i := 0; i < len(labels); i++ {
			queries = append(queries, query{
				Name: strings.Join(labels[i:], "."),
				Type: dns.TypeCAA,
			})
		}
	}

	for _, q := range queries {
		err := e.query(dnsClient, q.Name, q.Type)
		if err != nil {
			return err
		}
	}
	stats.results.With(prom.Labels{"result": "ok"}).Add(1)
	return nil
}

func (e Experiment) query(dnsClient *dns.Client, name string, typ uint16) error {
	if dnsClient == nil {
		return errors.New("query requires a non-nil dnsClient instance")
	}

	// Pick some servers based on the Experiment's Selector
	servers := e.Selector.PickServers()
	if len(servers) == 0 {
		// This shouldn't ever happen, but be defensive in case of a bug :-)
		return errors.New(
			"Experiment selector returned zero DNS server addresses from PickServers")
	}

	// Make the requsted query against each of the servers picked
	for _, serverAddr := range servers {
		if err := e.queryOne(dnsClient, serverAddr, name, typ); err != nil {
			return err
		}
	}
	return nil
}

func (e Experiment) queryOne(dnsClient *dns.Client, serverAddr, name string, typ uint16) error {
	typStr := dns.TypeToString[typ]
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), typ)
	in, rtt, err := dnsClient.Exchange(m, serverAddr)
	// TODO(@cpu): This should have a label for the serverAddr we queried.
	stats.queryTimes.With(prom.Labels{"type": typStr}).Observe(rtt.Seconds())
	if err != nil {
		if ne, ok := err.(*net.OpError); ok && ne.Timeout() {
			err = fmt.Errorf("timeout")
		} else if _, ok := err.(*net.OpError); ok {
			err = fmt.Errorf("net err")
		}
		// TODO(@cpu): This should have a label for the serverAddr we queried.
		stats.results.With(prom.Labels{"result": err.Error()}).Add(1)
		return fmt.Errorf("for %s: %s", typStr, err)
	} else if in.Rcode != dns.RcodeSuccess {
		rcodeStr := dns.RcodeToString[in.Rcode]
		// TODO(@cpu): This should have a label for the serverAddr we queried.
		stats.results.With(prom.Labels{"result": rcodeStr}).Add(1)
		return fmt.Errorf("for %s: %s", typStr, rcodeStr)
	}
	for _, answer := range in.Answer {
		if caaR, ok := answer.(*dns.CAA); ok && strings.ToLower(caaR.Tag) != caaR.Tag {
			return fmt.Errorf("tag mismatch for %s: %s", strings.ToLower(caaR.Tag), caaR)
		}
	}
	return nil
}

func Start(e Experiment, names <-chan string, wg *sync.WaitGroup) error {
	if err := e.Valid(); err != nil {
		return err
	}

	// Create & start a metrics server
	metricsServer := initMetrics(e.MetricsAddr)
	go func() {
		err := metricsServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Fatal("metrics server failed: %v", err)
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

func spawn(exp Experiment, dnsClient *dns.Client, names <-chan string, wg *sync.WaitGroup) {
	for i := 0; i < exp.Parallel; {
		for j := 0; j < exp.SpawnRate; i, j = i+1, j+1 {
			go func() {
				for name := range names {
					stats.attempts.Add(1)
					err := exp.runQueries(dnsClient, name)
					if err != nil {
						fmt.Printf("%s: %s\n", name, err)
					} else {
						fmt.Printf("%s: ok\n", name)
						stats.successes.Add(1)
					}
					wg.Done()
				}
			}()
		}
		time.Sleep(exp.SpawnInterval)
	}
}
