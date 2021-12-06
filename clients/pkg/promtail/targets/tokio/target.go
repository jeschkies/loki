package tokio

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	grpc "google.golang.org/grpc"
	"github.com/prometheus/common/model"

	"github.com/grafana/loki/clients/pkg/promtail/api"
	"github.com/grafana/loki/clients/pkg/promtail/scrapeconfig"
	"github.com/grafana/loki/clients/pkg/promtail/targets/target"
	"github.com/grafana/loki/clients/pkg/promtail/targets/tokio/proto/instrument"

	"github.com/grafana/loki/pkg/logproto"
)

type Target struct {
	logger    log.Logger
	handler   api.EntryHandler
	config    *scrapeconfig.TokioConfig

	client insturment.InstrumentClient
	ctx    context.Context
	cancel context.CancelFunc
	err    error
}

func NewTarget(
	logger  log.Logger,
	handler api.EntryHandler,
	config  *scrapeconfig.TokioConfig,
) (*Target, error) {
	cc , err := grpc.Dial(config.Addr)
	if err != nil {
		return nil, err
	}

	client := insturment.NewInstrumentClient(cc)
	ctx, cancel := context.WithCancel(context.Background())

	return &Target{
		logger:    logger,
		handler:   handler,
		config:    config,

		client: client,
		ctx: ctx,
		cancel: cancel,
	}, nil
}

func (t *Target) start() {
	watchUpdates, err := t.client.WatchUpdates(t.ctx, &insturment.InstrumentRequest{})
	if err != nil {
		t.err = err
		return
	}

	go func() {
		for t.ctx.Err() != nil {
			update, err := watchUpdates.Recv()
			if err != nil {
				level.Error(t.logger).Log("msg", "failed to pull update", "err", err)
				t.err = err
				return
			}

			line, err := json.Marshal(update)
			if err != nil {
				level.Error(t.logger).Log("msg", "failed to unmarshal update", "err", err)
				continue
			}

			ts := time.Now().UnixNano()

			t.handler.Chan() <- api.Entry{
				Labels: t.config.Labels.Clone(),
				Entry: logproto.Entry{
					Timestamp: time.Unix(0, ts),
					Line:      string(line),
				},
			}
		}
	}()
}

func (t *Target) Stop() {
	t.cancel()
	t.handler.Stop()
}

func (t *Target) Type() target.TargetType {
	return target.TokioTargetType
}

func (t *Target) DiscoveredLabels() model.LabelSet {
	return nil
}

func (t *Target) Labels() model.LabelSet {
	return t.config.Labels
}

func (t *Target) Ready() bool {
	return true 
}

func (t *Target) Details() interface{} {
	return map[string]string{}
}
