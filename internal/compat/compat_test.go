package compat

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

func TestParsePrometheus(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	payload := "# HELP requests_total requests\n# TYPE requests_total counter\nrequests_total{method=\"GET\",path=\"/v1/a\\\"b\"} 12 " + intString(now.UnixMilli()) + "\ntemperature 23.5\n"
	points, err := ParsePrometheus(strings.NewReader(payload), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Kind != telemetry.Counter || points[0].Labels["method"] != "GET" || points[0].TimestampMS != now.UnixMilli() || points[1].Value != 23.5 {
		t.Fatalf("points = %+v", points)
	}
}

func TestParseInflux(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	payload := "cpu,host=node-a,region=cn-east usage=12.5,cores=8i " + intString(now.UnixNano())
	points, err := ParseInflux(strings.NewReader(payload), now, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Labels["host"] != "node-a" || points[0].TimestampMS != now.UnixMilli() {
		t.Fatalf("points = %+v", points)
	}
}

func TestParseOTLPJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	payload := `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}}]},"scopeMetrics":[{"scope":{"name":"runtime","version":"1.0"},"metrics":[{"name":"process.cpu.utilization","unit":"1","gauge":{"dataPoints":[{"attributes":[{"key":"state","value":{"stringValue":"user"}}],"timeUnixNano":"` + intString(now.UnixNano()) + `","asDouble":0.42}]}},{"name":"requests","sum":{"aggregationTemporality":2,"isMonotonic":true,"dataPoints":[{"timeUnixNano":"` + intString(now.UnixNano()) + `","asInt":"7"}]}}]}]}]}`
	points, err := ParseOTLPJSON(strings.NewReader(payload), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Metric != "process.cpu.utilization" || points[0].Labels["service.name"] != "checkout" || points[0].Value != .42 || points[1].Kind != telemetry.Counter || points[1].Value != 7 {
		t.Fatalf("points = %+v", points)
	}
}

func TestParsersRejectEmptyOrInvalidPayloads(t *testing.T) {
	now := time.Now().UTC()
	if _, err := ParsePrometheus(strings.NewReader("# HELP x y\n"), now); err == nil {
		t.Fatal("expected empty Prometheus error")
	}
	if _, err := ParseInflux(strings.NewReader("bad-line"), now, "ns"); err == nil {
		t.Fatal("expected invalid Influx error")
	}
	if _, err := ParseOTLPJSON(strings.NewReader(`{"resourceMetrics":[]}`), now); err == nil {
		t.Fatal("expected empty OTLP error")
	}
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}
