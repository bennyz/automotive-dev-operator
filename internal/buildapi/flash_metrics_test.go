package buildapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"

	"github.com/gin-gonic/gin"
	. "github.com/onsi/ginkgo/v2" //nolint:revive // Dot import is standard for Ginkgo
	. "github.com/onsi/gomega"    //nolint:revive // Dot import is standard for Gomega
)

var _ = Describe("Flash Metrics", func() {
	Context("metrics endpoint", func() {
		It("should expose prometheus metrics at /metrics", func() {
			// Ensure at least one observation so the histogram appears
			FlashRequestDuration.WithLabelValues("/v1/flash", "200").Observe(0.001)

			router := gin.New()
			router.GET("/metrics", metricsHandler())

			req, err := http.NewRequest("GET", "/metrics", nil)
			Expect(err).NotTo(HaveOccurred())

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			body := w.Body.String()
			Expect(body).To(ContainSubstring("ado_flash_created_total"))
			Expect(body).To(ContainSubstring("ado_flash_request_duration_seconds"))
		})

		It("records request duration metric for flash route through router", func() {
			metricsTestRouter := gin.New()
			metricsTestRouter.POST("/v1/flash", flashMetricsMiddleware(), func(c *gin.Context) {
				c.Status(http.StatusAccepted)
			})
			metricsTestRouter.GET("/metrics", metricsHandler())

			reqBody := []byte(`{"name":"flash-metrics-test","imageRef":"quay.io/example/image:latest","clientConfig":"dummy"}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/flash", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			metricsTestRouter.ServeHTTP(w, req)
			Expect(w.Code).To(Equal(http.StatusAccepted))

			metricsReq, err := http.NewRequest(http.MethodGet, "/metrics", nil)
			Expect(err).NotTo(HaveOccurred())
			metricsResp := httptest.NewRecorder()
			metricsTestRouter.ServeHTTP(metricsResp, metricsReq)

			Expect(metricsResp.Code).To(Equal(http.StatusOK))
			metricsBody := metricsResp.Body.String()
			Expect(metricsBody).To(ContainSubstring(`ado_flash_request_duration_seconds_count{endpoint="/v1/flash",status_code="202"}`))
		})
	})

})
