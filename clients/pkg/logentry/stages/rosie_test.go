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
    prelude: |
        import date, net, time, word

        ip         = net.ip
        user       = word.any
        identd     = "-"
        timestamp  = date.day"/"date.month_name"/"date.year":"time.rfc2822 time.rfc2822_zone
    expression: >-
        ip identd user "["timestamp"]"
`

// `11.11.11.11 - frank [25/Jan/2000:14:00:01 -0500] "GET /1986.js HTTP/1.1" 200 932 "-" "Mozilla/5.0 (Windows; U; Windows NT 5.1; de; rv:1.9.1.7) Gecko/20091221 Firefox/3.5.7 GTB6"`

//"net.http_command_name net.path net.http_version"\"" [:digit:]+ [:digit:]

//expression: "^\\[(?P<timestamp>[\\w:/]+\\s[+\\-]\\d{4})\\] \"(?P<action>\\S+)\\s?(?P<path>\\S+)?\\s?(?P<protocol>\\S+)?\" (?P<status>\\d{3}|-) (?P<size>\\d+|-)\\s?\"?(?P<referer>[^\"]*)\"?\\s?\"?(?P<useragent>[^\"]*)?\"?$"

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
				"ip":        "11.11.11.11",
				"identd":    "-",
				"user":      "frank",
				"timestamp": "25/Jan/2000:14:00:01 -0500",
				"action":    "GET",
				"path":      "/1986.js",
				"protocol":  "HTTP/1.1",
				"status":    "200",
				"size":      "932",
				"referer":   "-",
				"useragent": "Mozilla/5.0 (Windows; U; Windows NT 5.1; de; rv:1.9.1.7) Gecko/20091221 Firefox/3.5.7 GTB6",
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
