package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
	prom "github.com/prometheus/client_golang/prometheus"
)

var debugAddr = flag.String("debugAddr", ":6363", "Timeout")
var timeout = flag.Duration("timeout", 30*time.Second, "Timeout")
var servers = flag.String("servers", "127.0.0.1:53", "Comma-separated list of DNS servers, round-robined")
var proto = flag.String("proto", "udp", "DNS proto (tcp or udp)")
var parallel = flag.Int("parallel", 5, "Number of parallel queries")
var spawnRate = flag.Int("spawnRate", 100, "Rate of spawning goroutines")
var spawnInterval = flag.Duration("spawnInterval", 1*time.Minute, "Interval on which to spawn goroutines")
var checkCAA = flag.Bool("checkCAA", false, "Whether to check CAA records")
var checkA = flag.Bool("checkA", false, "Whether to check A records")
var checkAAAA = flag.Bool("checkAAAA", false, "Whether to check AAAA records")
var checkDNAME = flag.Bool("checkDNAME", false, "Whether to check DNAME records")
var checkTXT = flag.Bool("checkTXT", false, "Whether to check TXT records")
var reverseNames = flag.Bool("reverse", false, "Whether to reverse input domains")
var c *dns.Client

var (
	resultStats = prom.NewCounterVec(prom.CounterOpts{
		Name: "results",
		Help: "lookup results",
	}, []string{"result"})
	attempts = prom.NewCounter(prom.CounterOpts{
		Name: "attempts",
		Help: "number of lookup attempts",
	})
	successes = prom.NewCounter(prom.CounterOpts{
		Name: "successes",
		Help: "number of lookup successes",
	})
	queryTimes = prom.NewSummaryVec(prom.SummaryOpts{
		Name: "queryTime",
		Help: "amount of time queries take (seconds)",
	}, []string{"type"})
	commandLine = prom.NewGaugeVec(prom.GaugeOpts{
		Name: "commandLine",
		Help: "command line",
	}, []string{"line"})
)

var serversSplit []string
var whichServer int
var whichMu sync.Mutex

// Pick a server, round-robin style. Concurrent-safe.
func pickServer() string {
	whichMu.Lock()
	which := whichServer
	whichServer++
	whichMu.Unlock()
	return serversSplit[which%len(serversSplit)]
}

func query(name string, typ uint16) error {
	typStr := dns.TypeToString[typ]
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), typ)
	in, rtt, err := c.Exchange(m, pickServer())
	queryTimes.With(prom.Labels{"type": typStr}).Observe(rtt.Seconds())
	if err != nil {
		if ne, ok := err.(*net.OpError); ok && ne.Timeout() {
			err = fmt.Errorf("timeout")
		} else if _, ok := err.(*net.OpError); ok {
			err = fmt.Errorf("net err")
		}
		resultStats.With(prom.Labels{"result": err.Error()}).Add(1)
		return fmt.Errorf("for %s: %s", typStr, err)
	} else if in.Rcode != dns.RcodeSuccess {
		rcodeStr := dns.RcodeToString[in.Rcode]
		resultStats.With(prom.Labels{"result": rcodeStr}).Add(1)
		return fmt.Errorf("for %s: %s", typStr, rcodeStr)
	} else if typ == dns.TypeDNAME && len(in.Answer) > 0 {
		for _, ans := range in.Answer {
			if _, ok := ans.(*dns.DNAME); ok {
				return fmt.Errorf("for DNAME: nonempty")
			}
		}
	}
	for _, answer := range in.Answer {
		if caaR, ok := answer.(*dns.CAA); ok && strings.ToLower(caaR.Tag) != caaR.Tag {
			return fmt.Errorf("tag mismatch for %s: %s", strings.ToLower(caaR.Tag), caaR)
		}
	}
	return nil
}

func tryAll(name string) error {
	var err error
	if *checkDNAME {
		err = query(name, dns.TypeDNAME)
		if err != nil {
			return err
		}
	}
	if *checkA {
		err = query(name, dns.TypeA)
		if err != nil {
			return err
		}
	}
	if *checkAAAA {
		if err := query(name, dns.TypeAAAA); err != nil {
			return err
		}
	}
	if *checkTXT {
		target := fmt.Sprintf("_acme-challenge.%s", name)
		if err := query(target, dns.TypeTXT); err != nil {
			return err
		}
	}

	labels := strings.Split(name, ".")
	if *checkCAA {
		for i := 0; i < len(labels); i++ {
			err = query(strings.Join(labels[i:], "."), dns.TypeCAA)
			if err != nil {
				return err
			}
		}
	}
	resultStats.With(prom.Labels{"result": "ok"}).Add(1)
	return nil
}

func spawn(names chan string, wg *sync.WaitGroup) {
	for i := 0; i < *parallel; {
		for j := 0; j < *spawnRate; i, j = i+1, j+1 {
			go func() {
				for name := range names {
					attempts.Add(1)
					err := tryAll(name)
					if err != nil {
						fmt.Printf("%s: %s\n", name, err)
					} else {
						fmt.Printf("%s: ok\n", name)
						successes.Add(1)
					}
					wg.Done()
				}
			}()
		}
		time.Sleep(*spawnInterval)
	}
}

func main() {
	commandLine.With(prom.Labels{
		"line": strings.Join(os.Args, " "),
	}).Set(1)
	flag.Parse()
	serversSplit = strings.Split(*servers, ",")
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		log.Fatal(err)
	}
	if *parallel > int(rLimit.Cur) {
		log.Fatalf("ulimit for nofile lower than -parallel: %d vs %d.",
			rLimit.Cur, *parallel)
	}

	prom.MustRegister(resultStats)
	prom.MustRegister(attempts)
	prom.MustRegister(successes)
	prom.MustRegister(queryTimes)

	b, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	c = &dns.Client{
		Net:         *proto,
		ReadTimeout: *timeout,
	}
	names := make(chan string)
	wg := sync.WaitGroup{}
	go spawn(names, &wg)
	http.Handle("/metrics", prom.Handler())
	go http.ListenAndServe(*debugAddr, nil)
	for _, name := range strings.Split(string(b), "\n") {
		if name != "" {
			wg.Add(1)
			if *reverseNames {
				name = ReverseName(name)
			}
			names <- name
		}
	}
	close(names)
	wg.Wait()
}

func ReverseName(domain string) string {
	labels := strings.Split(domain, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, ".")
}
