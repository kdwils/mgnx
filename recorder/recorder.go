package recorder

import (
	"fmt"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "mgnx"

// subSecondBuckets covers fast operations (torznab, scrape): 1ms → 2.048s.
var subSecondBuckets = prometheus.ExponentialBuckets(0.001, 2, 12)

// discoveryBuckets covers BEP-05 iterative lookups: 50ms → 25.6s.
// Chosen to span the full discovery_max_iterations × transaction_timeout range (e.g. 3 × 3s = 9s).
var discoveryBuckets = prometheus.ExponentialBuckets(0.05, 2, 10)

// fetchBuckets covers BEP-09 metadata fetches: 50ms → 12.8s.
// Chosen to span request_timeout (4s) with headroom.
var fetchBuckets = prometheus.ExponentialBuckets(0.05, 2, 9)

// peerCountBuckets spans typical DHT peer-set sizes: 0 → 233.
var peerCountBuckets = []float64{0, 1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233}

type Metrics struct {
	DHTDiscoveryQueueDepth prometheus.Gauge
	DHTQueueCapacity              prometheus.Gauge
	DHTDiscoveryQueueDroppedTotal prometheus.Counter
	DiscoveryWorkItemsTotal       *prometheus.CounterVec
	DiscoveryDurationSeconds      prometheus.Histogram

	CrawlerQueriesTotal       *prometheus.CounterVec
	CrawlerTraversalQueueSize *prometheus.GaugeVec
	CrawlerCooldownsActive    *prometheus.GaugeVec

	DHTMessagesInTotal  *prometheus.CounterVec
	DHTPacketsInTotal   prometheus.Counter
	DHTPacketsOutTotal  prometheus.Counter
	DHTRoutingTableSize prometheus.Gauge

	IndexerWorkersActive        prometheus.Gauge
	IndexerPeersProcessedTotal  *prometheus.CounterVec
	IndexerMetadataFetchedTotal prometheus.Counter
	IndexerMetadataFailedTotal  *prometheus.CounterVec
	IndexerFetchDurationSeconds prometheus.Histogram
	IndexerDBUpsertsTotal       prometheus.Counter
	TorrentsIndexedTotal        *prometheus.CounterVec

	// CrawlerSamplesTotal counts individual infohash samples from BEP-51 responses,
	// broken down by result. Use this to measure bloom filter saturation:
	// a high duplicate ratio means the crawler is re-seeing infohashes it already
	// enqueued this rotation window, confirming supply exhaustion.
	CrawlerSamplesTotal *prometheus.CounterVec

	// DiscoveredChannelDepth is the instantaneous depth of the discovered channel
	// between discovery workers and indexer workers. Near-zero means indexer
	// workers are starved waiting for peer sets.
	DiscoveredChannelDepth prometheus.Gauge

	// DiscoveryWorkersBusy is the number of discovery workers actively running
	// a get_peers lookup (not blocked waiting on the discovery queue).
	DiscoveryWorkersBusy prometheus.Gauge

	// IndexerWorkersBusy is the number of indexer workers actively processing
	// an infohash (not blocked waiting on the discovered channel).
	IndexerWorkersBusy prometheus.Gauge

	ScrapeCyclesTotal          prometheus.Counter
	ScrapeTorrentsUpdatedTotal prometheus.Counter
	ScrapeDeadDetectedTotal    prometheus.Counter
	ScrapeErrorsTotal          prometheus.Counter
	ScrapeDurationSeconds      prometheus.Histogram
	ScrapeHistoryPrunedTotal   prometheus.Counter

	TorznabRequestsTotal          prometheus.Counter
	TorznabRequestDurationSeconds prometheus.Histogram

	MetadataFetchAttemptsTotal   prometheus.Counter
	MetadataFetchSuccessTotal    prometheus.Counter
	MetadataFetchFailedTotal     *prometheus.CounterVec
	MetadataFetchDurationSeconds prometheus.Histogram

	TorrentsTotal        *prometheus.GaugeVec
	TorrentsRejectedTotal *prometheus.CounterVec
	IndexerDBErrorsTotal  *prometheus.CounterVec

	// IndexerDBQueryDurationSeconds tracks per-operation DB latency in the indexer.
	// Operations: lookup, upsert, insert_files, classify_update.
	// A slow lookup is especially costly because it runs on every infohash including duplicates.
	IndexerDBQueryDurationSeconds *prometheus.HistogramVec

	// IndexerRateLimiterWaitSeconds tracks how long the indexer blocks waiting for a
	// rate-limiter token before launching each BEP-09 peer goroutine. A high p99 here
	// means the rate limiter is the ceiling on fetch parallelism, not peer count or workers.
	IndexerRateLimiterWaitSeconds prometheus.Histogram

	// IndexerPeersPerInfohash is a histogram of how many peers were available when each
	// non-duplicate infohash entered the indexer. Zero-heavy distributions mean most
	// infohashes can never succeed at BEP-09 and the throughput cap is upstream in the DHT.
	IndexerPeersPerInfohash prometheus.Histogram
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
		DHTDiscoveryQueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dht_discovery_queue_depth",
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
		DiscoveryDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "discovery_duration_seconds",
			Help:      "get_peers lookup duration.",
			Buckets:   discoveryBuckets,
		}),
		CrawlerQueriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "crawler_queries_total",
			Help:      "Queries issued by crawler.",
		}, []string{"type", "mode", "worker"}),
		CrawlerTraversalQueueSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "crawler_traversal_queue_size",
			Help:      "Size of crawler traversal heap.",
		}, []string{"worker"}),
		CrawlerCooldownsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "crawler_cooldowns_active",
			Help:      "Number of nodes in cooldown.",
		}, []string{"worker"}),

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
		DHTRoutingTableSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dht_routing_table_size",
			Help:      "Routing table node count.",
		}),

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
			Buckets:   fetchBuckets,
		}),
		IndexerDBUpsertsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_db_upserts_total",
			Help:      "DB upsert operations.",
		}),
		TorrentsIndexedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "torrents_indexed_total",
			Help:      "Torrents indexed.",
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
			Buckets:   subSecondBuckets,
		}),
		ScrapeHistoryPrunedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_history_pruned_total",
			Help:      "Scrape history records pruned.",
		}),

		TorznabRequestsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "torznab_requests_total",
			Help:      "Total HTTP requests.",
		}),
		TorznabRequestDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "torznab_request_duration_seconds",
			Help:      "Request latency.",
			Buckets:   subSecondBuckets,
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
			Buckets:   fetchBuckets,
		}),

		TorrentsTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "torrents_total",
			Help:      "Number of torrents in the database by state.",
		}, []string{"state"}),
		TorrentsRejectedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "torrents_rejected_total",
			Help:      "Torrents rejected during classification.",
		}, []string{"reason"}),
		IndexerDBErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "indexer_db_errors_total",
			Help:      "Database errors in the indexer by operation.",
		}, []string{"operation"}),

		CrawlerSamplesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "crawler_samples_total",
			Help:      "BEP-51 infohash samples by result (new/duplicate/dropped). A high duplicate ratio confirms bloom filter saturation.",
		}, []string{"result"}),

		IndexerDBQueryDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "indexer_db_query_duration_seconds",
			Help:      "DB query latency inside the indexer by operation (lookup, upsert, insert_files, classify_update). A slow lookup is especially costly since it runs on every infohash.",
			Buckets:   subSecondBuckets,
		}, []string{"operation"}),
		IndexerRateLimiterWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "indexer_rate_limiter_wait_seconds",
			Help:      "Time blocked waiting for a rate-limiter token before launching each BEP-09 peer goroutine. High values mean the rate limiter is the throughput ceiling.",
			Buckets:   subSecondBuckets,
		}),
		IndexerPeersPerInfohash: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "indexer_peers_per_infohash",
			Help:      "Number of peers available when a non-duplicate infohash enters the indexer. Zero-heavy distributions mean BEP-09 is structurally supply-limited.",
			Buckets:   peerCountBuckets,
		}),
		DiscoveredChannelDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "discovered_channel_depth",
			Help:      "Instantaneous depth of the discovered channel between discovery workers and indexer workers. Near-zero means indexer workers are starved.",
		}),
		DiscoveryWorkersBusy: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "discovery_workers_busy",
			Help:      "Number of discovery workers actively running a get_peers lookup.",
		}),
		IndexerWorkersBusy: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "indexer_workers_busy",
			Help:      "Number of indexer workers actively processing an infohash.",
		}),
	}

	collectors := []prometheus.Collector{
		m.DHTDiscoveryQueueDepth,
		m.DHTQueueCapacity,
		m.DHTDiscoveryQueueDroppedTotal,
		m.DiscoveryWorkItemsTotal,
		m.DiscoveryDurationSeconds,
		m.CrawlerQueriesTotal,
		m.CrawlerTraversalQueueSize,
		m.CrawlerCooldownsActive,
		m.DHTMessagesInTotal,
		m.DHTPacketsInTotal,
		m.DHTPacketsOutTotal,
		m.DHTRoutingTableSize,
		m.IndexerWorkersActive,
		m.IndexerPeersProcessedTotal,
		m.IndexerMetadataFetchedTotal,
		m.IndexerMetadataFailedTotal,
		m.IndexerFetchDurationSeconds,
		m.IndexerDBUpsertsTotal,
		m.TorrentsIndexedTotal,
		m.ScrapeCyclesTotal,
		m.ScrapeTorrentsUpdatedTotal,
		m.ScrapeDeadDetectedTotal,
		m.ScrapeErrorsTotal,
		m.ScrapeDurationSeconds,
		m.ScrapeHistoryPrunedTotal,
		m.TorznabRequestsTotal,
		m.TorznabRequestDurationSeconds,
		m.MetadataFetchAttemptsTotal,
		m.MetadataFetchSuccessTotal,
		m.MetadataFetchFailedTotal,
		m.MetadataFetchDurationSeconds,
		m.TorrentsTotal,
		m.TorrentsRejectedTotal,
		m.IndexerDBErrorsTotal,
		m.CrawlerSamplesTotal,
		m.DiscoveredChannelDepth,
		m.DiscoveryWorkersBusy,
		m.IndexerWorkersBusy,
		m.IndexerDBQueryDurationSeconds,
		m.IndexerRateLimiterWaitSeconds,
		m.IndexerPeersPerInfohash,
	}

	for _, c := range collectors {
		if c == nil {
			log.Fatal(c)
		}
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

func (r *Recorder) SetDHTDiscoveryQueueDepth(v float64) {
	if r.m == nil {
		return
	}
	r.m.DHTDiscoveryQueueDepth.Set(v)
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

func (r *Recorder) ObserveDiscoveryDurationSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.DiscoveryDurationSeconds.Observe(v)
}

func (r *Recorder) IncCrawlerQueriesTotal(queryType string, mode string, worker int) {
	if r.m == nil {
		return
	}
	r.m.CrawlerQueriesTotal.WithLabelValues(queryType, mode, fmt.Sprintf("%d", worker)).Inc()
}

func (r *Recorder) SetCrawlerTraversalQueueSize(worker int, v float64) {
	if r.m == nil {
		return
	}
	r.m.CrawlerTraversalQueueSize.WithLabelValues(fmt.Sprintf("%d", worker)).Set(v)
}

func (r *Recorder) SetCrawlerCooldownsActive(worker int, v float64) {
	if r.m == nil {
		return
	}
	r.m.CrawlerCooldownsActive.WithLabelValues(fmt.Sprintf("%d", worker)).Set(v)
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

func (r *Recorder) SetDHTRoutingTableSize(v float64) {
	if r.m == nil {
		return
	}
	r.m.DHTRoutingTableSize.Set(v)
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

func (r *Recorder) IncTorrentsIndexedTotal(classType string) {
	if r.m == nil {
		return
	}
	r.m.TorrentsIndexedTotal.WithLabelValues(classType).Inc()
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

func (r *Recorder) IncTorznabRequestsTotal() {
	if r.m == nil {
		return
	}
	r.m.TorznabRequestsTotal.Inc()
}

func (r *Recorder) ObserveTorznabRequestDurationSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.TorznabRequestDurationSeconds.Observe(v)
}

func (r *Recorder) SetTorrentsTotal(state string, count float64) {
	if r.m == nil {
		return
	}
	r.m.TorrentsTotal.WithLabelValues(state).Set(count)
}

func (r *Recorder) IncTorrentsRejectedTotal(reason string) {
	if r.m == nil {
		return
	}
	r.m.TorrentsRejectedTotal.WithLabelValues(reason).Inc()
}

func (r *Recorder) IncIndexerDBErrorsTotal(operation string) {
	if r.m == nil {
		return
	}
	r.m.IndexerDBErrorsTotal.WithLabelValues(operation).Inc()
}

func (r *Recorder) AddCrawlerSamplesTotal(result string, n int) {
	if r.m == nil {
		return
	}
	r.m.CrawlerSamplesTotal.WithLabelValues(result).Add(float64(n))
}

func (r *Recorder) SetDiscoveredChannelDepth(v float64) {
	if r.m == nil {
		return
	}
	r.m.DiscoveredChannelDepth.Set(v)
}

func (r *Recorder) AddDiscoveryWorkersBusy(delta float64) {
	if r.m == nil {
		return
	}
	r.m.DiscoveryWorkersBusy.Add(delta)
}

func (r *Recorder) AddIndexerWorkersBusy(delta float64) {
	if r.m == nil {
		return
	}
	r.m.IndexerWorkersBusy.Add(delta)
}

func (r *Recorder) ObserveIndexerDBQueryDurationSeconds(operation string, v float64) {
	if r.m == nil {
		return
	}
	r.m.IndexerDBQueryDurationSeconds.WithLabelValues(operation).Observe(v)
}

func (r *Recorder) ObserveIndexerRateLimiterWaitSeconds(v float64) {
	if r.m == nil {
		return
	}
	r.m.IndexerRateLimiterWaitSeconds.Observe(v)
}

func (r *Recorder) ObserveIndexerPeersPerInfohash(v float64) {
	if r.m == nil {
		return
	}
	r.m.IndexerPeersPerInfohash.Observe(v)
}
