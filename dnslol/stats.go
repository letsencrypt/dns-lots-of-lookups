package dnslol

import (
	"net/http"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type dnsStats struct {
	attempts    *prom.CounterVec
	successes   *prom.CounterVec
	queryTimes  *prom.SummaryVec
	results     *prom.CounterVec
	commandLine *prom.GaugeVec
}

var (
	stats = &dnsStats{
		attempts: promauto.NewCounterVec(prom.CounterOpts{
			Name: "attempts",
			Help: "number of lookup attempts",
		}, []string{"server"}),
		successes: promauto.NewCounterVec(prom.CounterOpts{
			Name: "successes",
			Help: "number of lookup successes",
		}, []string{"server"}),
		queryTimes: promauto.NewSummaryVec(prom.SummaryOpts{
			Name: "queryTime",
			Help: "amount of time queries take (seconds)",
		}, []string{"server", "type"}),
		results: promauto.NewCounterVec(prom.CounterOpts{
			Name: "results",
			Help: "lookup results",
		}, []string{"server", "result"}),
		commandLine: promauto.NewGaugeVec(prom.GaugeOpts{
			Name: "commandLine",
			Help: "command line",
		}, []string{"line"}),
	}
)

// initMetrics creates an HTTP server listening on the provided addr with
// a Prometheus handler registered for the /metrics URL path. The return server
// is not started for the caller.
func initMetrics(addr string) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: promhttp.Handler(),
	}
}
