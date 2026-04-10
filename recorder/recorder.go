package recorder

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "mgnx"

var buckets = prometheus.ExponentialBuckets(0.001, 2, 12)

type Metrics struct {
	DHTNodesDiscoveredTotal       *prometheus.CounterVec
	DHTQueueDepth                 prometheus.Gauge
	DHTQueueCapacity              prometheus.Gauge
	DHTDiscoveryQueueDroppedTotal prometheus.Counter
	DiscoveryWorkItemsTotal       *prometheus.CounterVec
	DiscoveryWorkerBusy           prometheus.Gauge
	DiscoveryDurationSeconds      prometheus.Histogram
	CrawlerQueriesTotal           *prometheus.CounterVec
	CrawlerTraversalQueueSize     prometheus.Gauge
	CrawlerCooldownsActive        prometheus.Gauge
	CrawlerRoutingTableSize       prometheus.Gauge

	DHTMessagesInTotal          *prometheus.CounterVec
	DHTPacketsInTotal           prometheus.Counter
	DHTPacketsOutTotal          prometheus.Counter
	DHTTransactionsActive       prometheus.Gauge
	DHTTransactionTimeoutsTotal prometheus.Counter
	DHTRoutingTableSize         prometheus.Gauge
	DHTErrorsTotal              *prometheus.CounterVec

	IndexerWorkersActive        prometheus.Gauge
	IndexerPeersProcessedTotal  *prometheus.CounterVec
	IndexerMetadataFetchedTotal prometheus.Counter
	IndexerMetadataFailedTotal  *prometheus.CounterVec
	IndexerFetchDurationSeconds prometheus.Histogram
	IndexerDBUpsertsTotal       prometheus.Counter
	IndexerClassificationsTotal *prometheus.CounterVec

	ScrapeCyclesTotal          prometheus.Counter
	ScrapeTorrentsUpdatedTotal prometheus.Counter
	ScrapeDeadDetectedTotal    prometheus.Counter
	ScrapeErrorsTotal          prometheus.Counter
	ScrapeDurationSeconds      prometheus.Histogram
	ScrapeHistoryPrunedTotal   prometheus.Counter

	TorznabRequestsTotal          *prometheus.CounterVec
	TorznabRequestDurationSeconds *prometheus.HistogramVec
	TorznabErrorsTotal            *prometheus.CounterVec
	TorznabResultsTotal           prometheus.Counter

	MetadataFetchAttemptsTotal   prometheus.Counter
	MetadataFetchSuccessTotal    prometheus.Counter
	MetadataFetchFailedTotal     *prometheus.CounterVec
	MetadataFetchDurationSeconds prometheus.Histogram
}

type Recorder struct {
	m   *Metrics
	now func() time.Time
}

func (r *Recorder) GetMetrics() *Metrics {
	return r.m
}

func newMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		DHTNodesDiscoveredTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_nodes_discovered_total",
			Help:      "Nodes discovered from responses.",
		}, []string{"result"}),
		DHTQueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dht_queue_depth",
			Help:      "Current discovery queue length.",
		}),
		DHTQueueCapacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dht_queue_capacity",
			Help:      "Discovery queue capacity.",
		}),
		DHTDiscoveryQueueDroppedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_discovery_queue_dropped_total",
			Help:      "Samples dropped due to full queue.",
		}),
		DiscoveryWorkItemsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "discovery_work_items_total",
			Help:      "Discovery worker completions.",
		}, []string{"result"}),
		DiscoveryWorkerBusy: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "discovery_worker_busy",
			Help:      "Number of busy discovery workers.",
		}),
		DiscoveryDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "discovery_duration_seconds",
			Help:      "get_peers lookup duration.",
			Buckets:   buckets,
		}),
		CrawlerQueriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "crawler_queries_total",
			Help:      "Queries issued by crawler.",
		}, []string{"type"}),
		CrawlerTraversalQueueSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "crawler_traversal_queue_size",
			Help:      "Size of crawler traversal heap.",
		}),
		CrawlerCooldownsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "crawler_cooldowns_active",
			Help:      "Number of nodes in cooldown.",
		}),
		CrawlerRoutingTableSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "crawler_routing_table_size",
			Help:      "Number of nodes in routing table.",
		}),

		DHTMessagesInTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_messages_in_total",
			Help:      "DHT messages received.",
		}, []string{"type"}),
		DHTPacketsInTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_packets_in_total",
			Help:      "UDP packets received.",
		}),
		DHTPacketsOutTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_packets_out_total",
			Help:      "UDP packets sent.",
		}),
		DHTTransactionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dht_transactions_active",
			Help:      "Active transactions.",
		}),
		DHTTransactionTimeoutsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_transaction_timeouts_total",
			Help:      "Transaction timeouts.",
		}),
		DHTRoutingTableSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dht_routing_table_size",
			Help:      "Routing table node count.",
		}),
		DHTErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dht_errors_total",
			Help:      "Errors by type.",
		}, []string{"type"}),

		IndexerWorkersActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "indexer_workers_active",
			Help:      "Number of active indexer workers.",
		}),
		IndexerPeersProcessedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_peers_processed_total",
			Help:      "Peers processed from DHT.",
		}, []string{"result"}),
		IndexerMetadataFetchedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_metadata_fetched_total",
			Help:      "Successful metadata fetches.",
		}),
		IndexerMetadataFailedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_metadata_failed_total",
			Help:      "Failed metadata fetches.",
		}, []string{"reason"}),
		IndexerFetchDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "indexer_fetch_duration_seconds",
			Help:      "Metadata fetch duration.",
			Buckets:   buckets,
		}),
		IndexerDBUpsertsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_db_upserts_total",
			Help:      "DB upsert operations.",
		}),
		IndexerClassificationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_classifications_total",
			Help:      "Torrent classifications.",
		}, []string{"type"}),

		ScrapeCyclesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_cycles_total",
			Help:      "Scrape cycle executions.",
		}),
		ScrapeTorrentsUpdatedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_torrents_updated_total",
			Help:      "Torrents updated with scrape data.",
		}),
		ScrapeDeadDetectedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_dead_detected_total",
			Help:      "Torrents marked as dead.",
		}),
		ScrapeErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_errors_total",
			Help:      "Scrape operation errors.",
		}),
		ScrapeDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "scrape_duration_seconds",
			Help:      "Duration of scrape cycle.",
			Buckets:   buckets,
		}),
		ScrapeHistoryPrunedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_history_pruned_total",
			Help:      "Scrape history records pruned.",
		}),

		TorznabRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "torznab_requests_total",
			Help:      "HTTP requests by endpoint.",
		}, []string{"endpoint"}),
		TorznabRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "torznab_request_duration_seconds",
			Help:      "Request latency.",
			Buckets:   buckets,
		}, []string{"endpoint"}),
		TorznabErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "torznab_errors_total",
			Help:      "Errors by type.",
		}, []string{"type"}),
		TorznabResultsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "torznab_results_total",
			Help:      "Total results returned.",
		}),

		MetadataFetchAttemptsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "metadata_fetch_attempts_total",
			Help:      "Metadata fetch attempts.",
		}),
		MetadataFetchSuccessTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "metadata_fetch_success_total",
			Help:      "Successful metadata fetches.",
		}),
		MetadataFetchFailedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "metadata_fetch_failed_total",
			Help:      "Failed metadata fetches.",
		}, []string{"reason"}),
		MetadataFetchDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "metadata_fetch_duration_seconds",
			Help:      "Metadata fetch duration.",
			Buckets:   buckets,
		}),
	}

	collectors := []prometheus.Collector{
		m.DHTNodesDiscoveredTotal,
		m.DHTQueueDepth,
		m.DHTQueueCapacity,
		m.DHTDiscoveryQueueDroppedTotal,
		m.DiscoveryWorkItemsTotal,
		m.DiscoveryWorkerBusy,
		m.DiscoveryDurationSeconds,
		m.CrawlerQueriesTotal,
		m.CrawlerTraversalQueueSize,
		m.CrawlerCooldownsActive,
		m.CrawlerRoutingTableSize,
		m.DHTMessagesInTotal,
		m.DHTPacketsInTotal,
		m.DHTPacketsOutTotal,
		m.DHTTransactionsActive,
		m.DHTTransactionTimeoutsTotal,
		m.DHTRoutingTableSize,
		m.DHTErrorsTotal,
		m.IndexerWorkersActive,
		m.IndexerPeersProcessedTotal,
		m.IndexerMetadataFetchedTotal,
		m.IndexerMetadataFailedTotal,
		m.IndexerFetchDurationSeconds,
		m.IndexerDBUpsertsTotal,
		m.IndexerClassificationsTotal,
		m.ScrapeCyclesTotal,
		m.ScrapeTorrentsUpdatedTotal,
		m.ScrapeDeadDetectedTotal,
		m.ScrapeErrorsTotal,
		m.ScrapeDurationSeconds,
		m.ScrapeHistoryPrunedTotal,
		m.TorznabRequestsTotal,
		m.TorznabRequestDurationSeconds,
		m.TorznabErrorsTotal,
		m.TorznabResultsTotal,
		m.MetadataFetchAttemptsTotal,
		m.MetadataFetchSuccessTotal,
		m.MetadataFetchFailedTotal,
		m.MetadataFetchDurationSeconds,
	}

	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func NewNoOp() *Recorder {
	return &Recorder{now: time.Now}
}

func New(reg prometheus.Registerer) (*Recorder, error) {
	if reg == nil {
		return &Recorder{now: time.Now}, nil
	}

	m, err := newMetrics(reg)
	if err != nil {
		return nil, err
	}

	return &Recorder{m: m, now: time.Now}, nil
}

func (r *Recorder) IncDHTNodesDiscoveredTotal(result string) {
	if r.m == nil {
		return
	}
	r.m.DHTNodesDiscoveredTotal.WithLabelValues(result).Inc()
}

func (r *Recorder) SetDHTQueueDepth(v float64) {
	if r.m == nil {
		return
	}
	r.m.DHTQueueDepth.Set(v)
}

func (r *Recorder) SetDHTQueueCapacity(v float64) {
	if r.m == nil {
		return
	}
	r.m.DHTQueueCapacity.Set(v)
}

func (r *Recorder) IncDHTDiscoveryQueueDroppedTotal() {
	if r.m == nil {
		return
	}
	r.m.DHTDiscoveryQueueDroppedTotal.Inc()
}

func (r *Recorder) IncDiscoveryWorkItemsTotal(result string) {
	if r.m == nil {
		return
	}
	r.m.DiscoveryWorkItemsTotal.WithLabelValues(result).Inc()
}

func (r *Recorder) SetDiscoveryWorkerBusy(v float64) {
	if r.m == nil {
		return
	}
	r.m.DiscoveryWorkerBusy.Set(v)
}

func (r *Recorder) ObserveDiscoveryDurationSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.DiscoveryDurationSeconds.Observe(v)
}

func (r *Recorder) IncCrawlerQueriesTotal(queryType string) {
	if r.m == nil {
		return
	}
	r.m.CrawlerQueriesTotal.WithLabelValues(queryType).Inc()
}

func (r *Recorder) SetCrawlerTraversalQueueSize(v float64) {
	if r.m == nil {
		return
	}
	r.m.CrawlerTraversalQueueSize.Set(v)
}

func (r *Recorder) SetCrawlerCooldownsActive(v float64) {
	if r.m == nil {
		return
	}
	r.m.CrawlerCooldownsActive.Set(v)
}

func (r *Recorder) SetCrawlerRoutingTableSize(v float64) {
	if r.m == nil {
		return
	}
	r.m.CrawlerRoutingTableSize.Set(v)
}

func (r *Recorder) IncDHTMessagesInTotal(msgType string) {
	if r.m == nil {
		return
	}
	r.m.DHTMessagesInTotal.WithLabelValues(msgType).Inc()
}

func (r *Recorder) IncDHTPacketsInTotal() {
	if r.m == nil {
		return
	}
	r.m.DHTPacketsInTotal.Inc()
}

func (r *Recorder) IncDHTPacketsOutTotal() {
	if r.m == nil {
		return
	}
	r.m.DHTPacketsOutTotal.Inc()
}

func (r *Recorder) SetDHTTransactionsActive(v float64) {
	if r.m == nil {
		return
	}
	r.m.DHTTransactionsActive.Set(v)
}

func (r *Recorder) IncDHTTransactionTimeoutsTotal() {
	if r.m == nil {
		return
	}
	r.m.DHTTransactionTimeoutsTotal.Inc()
}

func (r *Recorder) SetDHTRoutingTableSize(v float64) {
	if r.m == nil {
		return
	}
	r.m.DHTRoutingTableSize.Set(v)
}

func (r *Recorder) IncDHTErrorsTotal(errType string) {
	if r.m == nil {
		return
	}
	r.m.DHTErrorsTotal.WithLabelValues(errType).Inc()
}

func (r *Recorder) SetIndexerWorkersActive(v float64) {
	if r.m == nil {
		return
	}
	r.m.IndexerWorkersActive.Set(v)
}

func (r *Recorder) IncIndexerPeersProcessedTotal(result string) {
	if r.m == nil {
		return
	}
	r.m.IndexerPeersProcessedTotal.WithLabelValues(result).Inc()
}

func (r *Recorder) IncIndexerMetadataFetchedTotal() {
	if r.m == nil {
		return
	}
	r.m.IndexerMetadataFetchedTotal.Inc()
}

func (r *Recorder) IncIndexerMetadataFailedTotal(reason string) {
	if r.m == nil {
		return
	}
	r.m.IndexerMetadataFailedTotal.WithLabelValues(reason).Inc()
}

func (r *Recorder) ObserveIndexerFetchDurationSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.IndexerFetchDurationSeconds.Observe(v)
}

func (r *Recorder) IncIndexerDBUpsertsTotal() {
	if r.m == nil {
		return
	}
	r.m.IndexerDBUpsertsTotal.Inc()
}

func (r *Recorder) IncIndexerClassificationsTotal(classType string) {
	if r.m == nil {
		return
	}
	r.m.IndexerClassificationsTotal.WithLabelValues(classType).Inc()
}

func (r *Recorder) IncScrapeCyclesTotal() {
	if r.m == nil {
		return
	}
	r.m.ScrapeCyclesTotal.Inc()
}

func (r *Recorder) IncScrapeTorrentsUpdatedTotal() {
	if r.m == nil {
		return
	}
	r.m.ScrapeTorrentsUpdatedTotal.Inc()
}

func (r *Recorder) IncScrapeDeadDetectedTotal() {
	if r.m == nil {
		return
	}
	r.m.ScrapeDeadDetectedTotal.Inc()
}

func (r *Recorder) IncScrapeErrorsTotal() {
	if r.m == nil {
		return
	}
	r.m.ScrapeErrorsTotal.Inc()
}

func (r *Recorder) ObserveScrapeDurationSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.ScrapeDurationSeconds.Observe(v)
}

func (r *Recorder) IncScrapeHistoryPrunedTotal() {
	if r.m == nil {
		return
	}
	r.m.ScrapeHistoryPrunedTotal.Inc()
}

func (r *Recorder) IncTorznabRequestsTotal(endpoint string) {
	if r.m == nil {
		return
	}
	r.m.TorznabRequestsTotal.WithLabelValues(endpoint).Inc()
}

func (r *Recorder) ObserveTorznabRequestDurationSeconds(endpoint string, v float64) {
	if r.m == nil {
		return
	}
	r.m.TorznabRequestDurationSeconds.WithLabelValues(endpoint).Observe(v)
}

func (r *Recorder) IncTorznabErrorsTotal(errType string) {
	if r.m == nil {
		return
	}
	r.m.TorznabErrorsTotal.WithLabelValues(errType).Inc()
}

func (r *Recorder) IncTorznabResultsTotal() {
	if r.m == nil {
		return
	}
	r.m.TorznabResultsTotal.Inc()
}

func (r *Recorder) IncMetadataFetchAttemptsTotal() {
	if r.m == nil {
		return
	}
	r.m.MetadataFetchAttemptsTotal.Inc()
}

func (r *Recorder) IncMetadataFetchSuccessTotal() {
	if r.m == nil {
		return
	}
	r.m.MetadataFetchSuccessTotal.Inc()
}

func (r *Recorder) IncMetadataFetchFailedTotal(reason string) {
	if r.m == nil {
		return
	}
	r.m.MetadataFetchFailedTotal.WithLabelValues(reason).Inc()
}

func (r *Recorder) ObserveMetadataFetchDurationSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.MetadataFetchDurationSeconds.Observe(v)
}
