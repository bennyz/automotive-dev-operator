package buildapi

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	flashMetricsNamespace = "ado"
	flashMetricsSubsystem = "flash"
)

var (
	// FlashCreatedTotal counts standalone flash operations created via the REST API.
	FlashCreatedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: flashMetricsNamespace,
			Subsystem: flashMetricsSubsystem,
			Name:      "created_total",
			Help:      "Total number of flash operations created",
		},
	)

	// FlashRequestDuration tracks flash API request duration by endpoint and status code.
	FlashRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: flashMetricsNamespace,
			Subsystem: flashMetricsSubsystem,
			Name:      "request_duration_seconds",
			Help:      "Flash API request duration in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"endpoint", "status_code"},
	)
)

func init() {
	prometheus.MustRegister(
		FlashCreatedTotal,
		FlashRequestDuration,
	)
}

func flashMetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start).Seconds()
		endpoint := c.FullPath()
		statusCode := fmt.Sprintf("%d", c.Writer.Status())
		FlashRequestDuration.WithLabelValues(endpoint, statusCode).Observe(duration)
	}
}

func metricsHandler() gin.HandlerFunc {
	h := promhttp.Handler()
	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
