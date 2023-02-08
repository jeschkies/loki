package stages

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	util_log "github.com/grafana/loki/pkg/util/log"

	"github.com/stretchr/testify/assert"
)

var testRosieYamlSingleStageWithoutSource = `
pipeline_stages:
- rosie:
    expression: "net.ip \"-\" word.any"
`

func TestPipeline_Rosie(t *testing.T) {

	tests := map[string]struct {
		config          string
		entry           string
		expectedExtract map[string]interface{}
	}{
		"successfully run a pipeline with 1 rosie stage without source": {
			testRosieYamlSingleStageWithoutSource,
			testRegexLogLine,
			map[string]interface{}{
				"ip": "11.11.11.11",
			},
		},
	}

	for testName, testData := range tests {
		testData := testData

		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			pl, err := NewPipeline(util_log.Logger, loadConfig(testData.config), nil, prometheus.DefaultRegisterer)
			if err != nil {
				t.Fatal(err)
			}

			out := processEntries(pl, newEntry(nil, nil, testData.entry, time.Now()))[0]
			assert.Equal(t, testData.expectedExtract, out.Extracted)
		})
	}
}
