package triggers

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	pullRequestMonitorActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "active",
		Help:      "Number of active pull request monitors.",
	})
	pullRequestMonitorPolls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "polls_total",
		Help:      "Number of GitHub polls by endpoint and result.",
	}, []string{"endpoint", "result"})
	pullRequestMonitorNotModified = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "not_modified_total",
		Help:      "Number of GitHub poll responses with HTTP status 304.",
	}, []string{"endpoint"})
	pullRequestMonitorPollLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "poll_latency_seconds",
		Help:      "GitHub poll latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"endpoint"})
	pullRequestMonitorFeedback = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "feedback_total",
		Help:      "Number of feedback events dispatched or ignored.",
	}, []string{"result"})
	pullRequestMonitorErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "errors_total",
		Help:      "Number of pull request monitor errors by operation.",
	}, []string{"operation"})
	pullRequestMonitorRateRemaining = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "rate_remaining",
		Help:      "Latest observed GitHub API rate-limit remaining value.",
	}, []string{"endpoint"})
	pullRequestMonitorTerminalStops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gratefulagents",
		Subsystem: "pull_request_monitor",
		Name:      "terminal_stops_total",
		Help:      "Number of monitors stopped because the pull request reached a terminal state.",
	}, []string{"reason"})
	activeMonitorMu   sync.Mutex
	activeMonitorKeys = map[string]struct{}{}
)

func init() {
	crmetrics.Registry.MustRegister(
		pullRequestMonitorActive,
		pullRequestMonitorPolls,
		pullRequestMonitorNotModified,
		pullRequestMonitorPollLatency,
		pullRequestMonitorFeedback,
		pullRequestMonitorErrors,
		pullRequestMonitorRateRemaining,
		pullRequestMonitorTerminalStops,
	)
}

func observeMonitorStarted(key string) {
	activeMonitorMu.Lock()
	defer activeMonitorMu.Unlock()
	activeMonitorKeys[key] = struct{}{}
	pullRequestMonitorActive.Set(float64(len(activeMonitorKeys)))
}

func observeMonitorStopped(key string) {
	activeMonitorMu.Lock()
	defer activeMonitorMu.Unlock()
	delete(activeMonitorKeys, key)
	pullRequestMonitorActive.Set(float64(len(activeMonitorKeys)))
}

func observePoll(endpoint, result string, duration time.Duration) {
	pullRequestMonitorPolls.WithLabelValues(endpoint, result).Inc()
	pullRequestMonitorPollLatency.WithLabelValues(endpoint).Observe(duration.Seconds())
}

func observeNotModified(endpoint string) {
	pullRequestMonitorNotModified.WithLabelValues(endpoint).Inc()
}

func observeFeedbackDispatched() {
	pullRequestMonitorFeedback.WithLabelValues("dispatched").Inc()
}

func observeFeedbackIgnored() {
	pullRequestMonitorFeedback.WithLabelValues("ignored").Inc()
}

func observeMonitorError(operation string) {
	pullRequestMonitorErrors.WithLabelValues(operation).Inc()
}

func observeRateRemaining(endpoint string, remaining int) {
	pullRequestMonitorRateRemaining.WithLabelValues(endpoint).Set(float64(remaining))
}

func observeTerminalStop(reason string) {
	pullRequestMonitorTerminalStops.WithLabelValues(reason).Inc()
}
