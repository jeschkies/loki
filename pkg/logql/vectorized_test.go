package logql

import (
	"archive/zip"
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/pkg/logql/log"
)

func Test_EntryVec(t * testing.T) {
	haystack := []byte(`96.81.153.18 - predovic2578 [02/May/2023:10:21:20 +0000] "PATCH /extend HTTP/1.0" 502 12210
203.48.225.9 - - [02/May/2023:10:21:20 +0000] "PATCH /back-end/facilitate/mission-critical/orchestrate HTTP/2.0" 204 15545
254.4.4.148 - - [02/May/2023:10:21:20 +0000] "DELETE /compelling/strategic/integrated HTTP/1.0" 500 15822
34.194.247.251 - - [02/May/2023:10:21:20 +0000] "PATCH /niches/mindshare HTTP/2.0" 405 27960
180.103.196.205 - strosin3705 [02/May/2023:10:21:20 +0000] "PATCH /proactive/e-markets/out-of-the-box/roi HTTP/1.1" 502 27669
20.34.120.221 - - [02/May/2023:10:21:20 +0000] "DELETE /eyeballs/expedite/proactive HTTP/1.0" 301 22049
63.216.162.248 - keeling6124 [02/May/2023:10:21:20 +0000] "PATCH /reinvent HTTP/2.0" 501 13633
253.252.67.36 - - [02/May/2023:10:21:20 +0000] "GET /drive/communities/reintermediate HTTP/2.0" 416 25477
207.148.140.17 - - [02/May/2023:10:21:20 +0000] "PATCH /orchestrate/synergies/back-end HTTP/1.1" 406 20607
226.80.242.123 - - [02/May/2023:10:21:20 +0000] "POST /synergies HTTP/2.0" 203 2523
`)

	vecs := NewEntriesVec(haystack)
	starts := vecs[1].Int()
	require.Equal(t, len(starts), 10)
	require.Equal(t, string(haystack[starts[0]:starts[1]]), "96.81.153.18 - predovic2578 [02/May/2023:10:21:20 +0000] \"PATCH /extend HTTP/1.0\" 502 12210\n")
	require.Equal(t, string(haystack[starts[1]:starts[2]]), "203.48.225.9 - - [02/May/2023:10:21:20 +0000] \"PATCH /back-end/facilitate/mission-critical/orchestrate HTTP/2.0\" 204 15545\n")
	require.Equal(t, string(haystack[starts[2]:starts[3]]), "254.4.4.148 - - [02/May/2023:10:21:20 +0000] \"DELETE /compelling/strategic/integrated HTTP/1.0\" 500 15822\n")
	require.Equal(t, string(haystack[starts[3]:starts[4]]), "34.194.247.251 - - [02/May/2023:10:21:20 +0000] \"PATCH /niches/mindshare HTTP/2.0\" 405 27960\n")
	require.Equal(t, string(haystack[starts[4]:starts[5]]), "180.103.196.205 - strosin3705 [02/May/2023:10:21:20 +0000] \"PATCH /proactive/e-markets/out-of-the-box/roi HTTP/1.1\" 502 27669\n")
	require.Equal(t, string(haystack[starts[5]:starts[6]]), "20.34.120.221 - - [02/May/2023:10:21:20 +0000] \"DELETE /eyeballs/expedite/proactive HTTP/1.0\" 301 22049\n")
	require.Equal(t, string(haystack[starts[6]:starts[7]]), "63.216.162.248 - keeling6124 [02/May/2023:10:21:20 +0000] \"PATCH /reinvent HTTP/2.0\" 501 13633\n")
	require.Equal(t, string(haystack[starts[7]:starts[8]]), "253.252.67.36 - - [02/May/2023:10:21:20 +0000] \"GET /drive/communities/reintermediate HTTP/2.0\" 416 25477\n")
	require.Equal(t, string(haystack[starts[8]:starts[9]]), "207.148.140.17 - - [02/May/2023:10:21:20 +0000] \"PATCH /orchestrate/synergies/back-end HTTP/1.1\" 406 20607\n")
	require.Equal(t, string(haystack[starts[9]:]), "226.80.242.123 - - [02/May/2023:10:21:20 +0000] \"POST /synergies HTTP/2.0\" 203 2523\n")

	require.Equal(t, len(vecs[2].Int()), 10)
}

func Test_VecFilter(t *testing.T) {
	haystack := []byte(`96.81.153.18 - predovic2578 [02/May/2023:10:21:20 +0000] "PATCH /extend HTTP/1.0" 502 12210
203.48.225.9 - - [02/May/2023:10:21:20 +0000] "PATCH /back-end/facilitate/mission-critical/orchestrate HTTP/2.0" 204 15545
254.4.4.148 - - [02/May/2023:10:21:20 +0000] "DELETE /compelling/strategic/integrated HTTP/1.0" 500 15822
34.194.247.251 - - [02/May/2023:10:21:20 +0000] "PATCH /niches/mindshare HTTP/2.0" 405 27960
180.103.196.205 - strosin3705 [02/May/2023:10:21:20 +0000] "PATCH /proactive/e-markets/out-of-the-box/roi HTTP/1.1" 502 27669
20.34.120.221 - - [02/May/2023:10:21:20 +0000] "DELETE /eyeballs/expedite/proactive HTTP/1.0" 301 22049
63.216.162.248 - keeling6124 [02/May/2023:10:21:20 +0000] "PATCH /reinvent HTTP/2.0" 501 13633
253.252.67.36 - - [02/May/2023:10:21:20 +0000] "GET /drive/communities/reintermediate HTTP/2.0" 416 25477
207.148.140.17 - - [02/May/2023:10:21:20 +0000] "PATCH /orchestrate/synergies/back-end HTTP/1.1" 406 20607
226.80.242.123 - - [02/May/2023:10:21:20 +0000] "POST /synergies HTTP/2.0" 203 2523
`)

	vecs := NewEntriesVec(haystack)
	batch := NewBatch(vecs)
	updated := VecFilter(batch, 0, []byte("DELETE"))
	require.ElementsMatch(t, updated.Selection(), []int{2, 5})
}

func Benchmark_PipelineLarge(b *testing.B) {
	//needle := []byte(`77.47.98.232 - - [02/May/2023:10:20:14 +0000] "GET /empower/e-business/whiteboard`)
	needle := []byte(`whiteboard`)
	haystack, err := loadHaystack("big.log")
	require.NoError(b, err)

	// TODO: maybe we want to use the entry iterator
	// https://github.com/grafana/loki/blob/main/pkg/ingester/stream_test.go#L186
	stages := []log.Stage{
		mustFilter(log.NewFilter(string(needle), labels.MatchEqual)).ToStage(),
	}
	p := log.NewPipeline(stages)
	lbs := labels.FromStrings("cluster", "ops-tool1",
		"name", "querier",
	)
	sp := p.ForStream(lbs)

	b.Run("iterative", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			var iterator Iter = iterImpl{
				scanner: bufio.NewScanner(bytes.NewReader(haystack)),
				labels:  lbs,
			}
			for iterator.Next() {
				entry := iterator.Entry()
				sp.Process(0, entry.line)
				//lines += bool2int(matches)
			}
		}
	})

	b.Run("vectorized", func(b *testing.B) {
		vecs := NewEntriesVec(haystack)
		require.Equal(b, len(vecs[1].Int()), 5196783)
		require.Equal(b, len(vecs[2].Int()), 5196783)
		b.ResetTimer()
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			batch := NewBatch(vecs)
			updated := VecFilter(batch, 0, needle)
			require.Len(b, updated.Selection(), 76416)
		}
	})
}

type Entry struct {
	ts     int64
	line   []byte
	labels labels.Labels
}

type Iter interface {
	Next() bool
	Entry() Entry
}

type iterImpl struct {
	scanner *bufio.Scanner
	labels  labels.Labels
}

func (i iterImpl) Next() bool {
	return i.scanner.Scan()
}

func (i iterImpl) Entry() Entry {
	return Entry{
		ts:     0,
		line:   i.scanner.Bytes(),
		labels: i.labels,
	}
}

func loadHaystack(name string) ([]byte, error) {
	r, err := zip.OpenReader("testdata/data.zip")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func mustFilter(f log.Filterer, err error) log.Filterer {
	if err != nil {
		panic(err)
	}
	return f
}
