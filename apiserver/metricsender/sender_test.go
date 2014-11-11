// Copyright 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package metricsender_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/apiserver/metricsender"
	"github.com/juju/juju/apiserver/metricsender/wireformat"
	"github.com/juju/juju/cert"
	jujutesting "github.com/juju/juju/juju/testing"
	"github.com/juju/juju/state"
	"github.com/juju/juju/testing/factory"
)

type SenderSuite struct {
	jujutesting.JujuConnSuite
}

var _ = gc.Suite(&SenderSuite{})

func createCerts(c *gc.C, serverName string) (*x509.CertPool, tls.Certificate) {
	certCaPem, keyCaPem, err := cert.NewCA("sender-test", time.Now().Add(time.Minute))
	c.Assert(err, gc.IsNil)
	certPem, keyPem, err := cert.NewServer(certCaPem, keyCaPem, time.Now().Add(time.Minute), []string{serverName})
	c.Assert(err, gc.IsNil)
	cert, err := tls.X509KeyPair([]byte(certPem), []byte(keyPem))
	c.Assert(err, gc.IsNil)
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM([]byte(certCaPem))
	return certPool, cert
}

// startServer starts a server with TLS and the specified handler, returning a
// function that should be run at the end of the test to clean up.
func (s *SenderSuite) startServer(c *gc.C, handler http.Handler) func() {
	ts := httptest.NewUnstartedServer(handler)
	certPool, cert := createCerts(c, "127.0.0.1")
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	ts.StartTLS()
	cleanup := metricsender.PatchHostAndCertPool(ts.URL, certPool)
	return func() {
		ts.Close()
		cleanup()
	}
}

var _ metricsender.MetricSender = (*metricsender.DefaultSender)(nil)

// TestDefaultSender check that if the default sender
// is in use metrics get sent
func (s *SenderSuite) TestDefaultSender(c *gc.C) {
	metricCount := 3
	unit := s.Factory.MakeUnit(c, &factory.UnitParams{SetCharmURL: true})
	expectedCharmUrl, _ := unit.CharmURL()

	receiverChan := make(chan wireformat.MetricBatch, metricCount)
	cleanup := s.startServer(c, testHandler(c, receiverChan, nil))
	defer cleanup()

	now := time.Now()
	metrics := make([]*state.MetricBatch, metricCount)
	for i, _ := range metrics {
		metrics[i] = s.Factory.MakeMetric(c, &factory.MetricParams{Unit: unit, Sent: false, Time: &now})
	}
	var sender metricsender.DefaultSender
	err := metricsender.SendMetrics(s.State, &sender, 10)
	c.Assert(err, gc.IsNil)

	c.Assert(receiverChan, gc.HasLen, metricCount)
	close(receiverChan)
	for batch := range receiverChan {
		c.Assert(batch.CharmUrl, gc.Equals, expectedCharmUrl.String())
	}

	for _, metric := range metrics {
		m, err := s.State.MetricBatch(metric.UUID())
		c.Assert(err, gc.IsNil)
		c.Assert(m.Sent(), jc.IsTrue)
	}
}

// StatusMap defines a type for a function that returns the status and information for a specified unit.
type StatusMap func(unitName string) (unit string, status string, info string)

func errorHandler(c *gc.C, errorCode int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(errorCode)
	}
}

func testHandler(c *gc.C, batches chan<- wireformat.MetricBatch, statusMap StatusMap) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.Method, gc.Equals, "POST")
		dec := json.NewDecoder(r.Body)
		enc := json.NewEncoder(w)
		var incoming []wireformat.MetricBatch
		err := dec.Decode(&incoming)
		c.Assert(err, gc.IsNil)

		var resp = make(wireformat.EnvironmentResponses)
		for _, batch := range incoming {
			c.Logf("received metrics batch: %+v", batch)

			resp.Ack(batch.EnvUUID, batch.UUID)

			if statusMap != nil {
				unitName, status, info := statusMap(batch.UnitName)
				resp.SetStatus(batch.EnvUUID, unitName, status, info)
			}

			select {
			case batches <- batch:
			default:
			}
		}
		uuid, err := utils.NewUUID()
		c.Assert(err, gc.IsNil)
		err = enc.Encode(wireformat.Response{
			UUID:         uuid.String(),
			EnvResponses: resp,
		})
		c.Assert(err, gc.IsNil)

	}
}

// TestErrorCodes checks that for a set of error codes SendMetrics returns an
// error and metrics are marked as not being sent
func (s *SenderSuite) TestErrorCodes(c *gc.C) {
	tests := []struct {
		errorCode   int
		expectedErr string
	}{
		{http.StatusBadRequest, "failed to send metrics http 400"},
		{http.StatusServiceUnavailable, "failed to send metrics http 503"},
		{http.StatusMovedPermanently, "failed to send metrics http 301"},
	}
	unit := s.Factory.MakeUnit(c, &factory.UnitParams{SetCharmURL: true})

	for _, test := range tests {
		killServer := s.startServer(c, errorHandler(c, test.errorCode))

		now := time.Now()
		batches := make([]*state.MetricBatch, 3)
		for i, _ := range batches {
			batches[i] = s.Factory.MakeMetric(c, &factory.MetricParams{Unit: unit, Sent: false, Time: &now})
		}
		var sender metricsender.DefaultSender
		err := metricsender.SendMetrics(s.State, &sender, 10)
		c.Assert(err, gc.ErrorMatches, test.expectedErr)
		for _, batch := range batches {
			m, err := s.State.MetricBatch(batch.UUID())
			c.Assert(err, gc.IsNil)
			c.Assert(m.Sent(), jc.IsFalse)
		}
		killServer()
	}
}

// TestMeterStatus checks that the meter status information returned
// by the collector service is propagated to the unit.
// is in use metrics get sent
func (s *SenderSuite) TestMeterStatus(c *gc.C) {
	unit := s.Factory.MakeUnit(c, &factory.UnitParams{SetCharmURL: true})

	statusFunc := func(unitName string) (string, string, string) {
		return unitName, "GREEN", ""
	}

	cleanup := s.startServer(c, testHandler(c, nil, statusFunc))
	defer cleanup()

	_ = s.Factory.MakeMetric(c, &factory.MetricParams{Unit: unit, Sent: false})

	status, info, err := unit.GetMeterStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, "NOT SET")
	c.Assert(info, gc.Equals, "")

	var sender metricsender.DefaultSender
	err = metricsender.SendMetrics(s.State, &sender, 10)
	c.Assert(err, gc.IsNil)

	status, info, err = unit.GetMeterStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, "GREEN")
	c.Assert(info, gc.Equals, "")
}

// TestMeterStatusInvalid checks that the metric sender deals with invalid
// meter status data properly.
func (s *SenderSuite) TestMeterStatusInvalid(c *gc.C) {
	service := s.Factory.MakeService(c, nil)
	unit1 := s.Factory.MakeUnit(c, &factory.UnitParams{Service: service, SetCharmURL: true})
	unit2 := s.Factory.MakeUnit(c, &factory.UnitParams{Service: service, SetCharmURL: true})
	unit3 := s.Factory.MakeUnit(c, &factory.UnitParams{Service: service, SetCharmURL: true})

	statusFunc := func(unitName string) (string, string, string) {
		switch unitName {
		case unit1.Name():
			// valid meter status
			return unitName, "GREEN", ""
		case unit2.Name():
			// invalid meter status
			return unitName, "blah", ""
		case unit3.Name():
			// invalid unit name
			return "no-such-unit", "GREEN", ""
		default:
			return unitName, "GREEN", ""
		}
	}

	cleanup := s.startServer(c, testHandler(c, nil, statusFunc))
	defer cleanup()

	_ = s.Factory.MakeMetric(c, &factory.MetricParams{Unit: unit1, Sent: false})
	_ = s.Factory.MakeMetric(c, &factory.MetricParams{Unit: unit2, Sent: false})
	_ = s.Factory.MakeMetric(c, &factory.MetricParams{Unit: unit3, Sent: false})

	for _, unit := range []*state.Unit{unit1, unit2, unit3} {
		status, info, err := unit.GetMeterStatus()
		c.Assert(err, gc.IsNil)
		c.Assert(status, gc.Equals, "NOT SET")
		c.Assert(info, gc.Equals, "")
	}

	var sender metricsender.DefaultSender
	err := metricsender.SendMetrics(s.State, &sender, 10)
	c.Assert(err, gc.IsNil)

	status, info, err := unit1.GetMeterStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, "GREEN")
	c.Assert(info, gc.Equals, "")

	status, info, err = unit2.GetMeterStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, "NOT SET")
	c.Assert(info, gc.Equals, "")

	status, info, err = unit3.GetMeterStatus()
	c.Assert(err, gc.IsNil)
	c.Assert(status, gc.Equals, "NOT SET")
	c.Assert(info, gc.Equals, "")

}
