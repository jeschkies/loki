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

func Benchmark_PipelineLarge(b *testing.B) {
	//needle := []byte(`77.47.98.232 - - [02/May/2023:10:20:14 +0000] "GET /empower/e-business/whiteboard`)
	needle := []byte(`whiteboard`)
	haystack, err := loadHaystack("big.log")
	require.NoError(b, err)

	reader := bytes.NewReader(haystack)
	scanner := bufio.NewScanner(reader)

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
	var iterator Iter = iterImpl{scanner: scanner, labels: lbs}

	b.Run("iterative", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			reader.Seek(0, io.SeekStart) //nolint:errcheck
			for iterator.Next() {
				entry := iterator.Entry()
				sp.Process(0, entry.line)
			}
		}
	})

	batch := NewBatch([]Vec{bytesVec(haystack)})
	b.Run("vectorized", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			VecFilter(batch, 0, needle)
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
