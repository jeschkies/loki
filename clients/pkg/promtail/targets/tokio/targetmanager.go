package tokio

import (
	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/grafana/loki/clients/pkg/logentry/stages"
	"github.com/grafana/loki/clients/pkg/promtail/api"
	"github.com/grafana/loki/clients/pkg/promtail/scrapeconfig"
	"github.com/grafana/loki/clients/pkg/promtail/targets/target"
)

type TargetManager struct {
	logger  log.Logger
	targets map[string]*Target
}

func NewTargetManager(
	reg prometheus.Registerer,
	logger log.Logger,
	scrapeConfigs []scrapeconfig.Config,
	pushClient api.EntryHandler,
) (*TargetManager, error) {
	tm := &TargetManager{
		logger: logger,
		targets: make(map[string]*Target),
	}

	for _, cfg := range scrapeConfigs {
		if cfg.TokioConfig == nil {
			continue
		}

		pipeline, err := stages.NewPipeline(log.With(logger, "component", "tokio_pipeline"), cfg.PipelineStages, &cfg.JobName, reg)
		if err != nil {
			return nil, err
		}
		t, err := NewTarget(log.With(logger, "target", "tokio"), pipeline.Wrap(pushClient), cfg.TokioConfig)
		if err != nil {
			return nil, err
		}
		tm.targets[cfg.JobName] = t
	}

	return tm, nil
}

func (tm *TargetManager) Ready() bool {
	for _, t := range tm.targets {
		if t.Ready() {
			return true
		}
	}
	return false
}

func (tm *TargetManager) Stop() {
	for _, t := range tm.targets {
		t.Stop()
	}
}

func (tm *TargetManager) ActiveTargets() map[string][]target.Target {
	result := make(map[string][]target.Target, len(tm.targets))
	for k, v := range tm.targets {
		if v.Ready() {
			result[k] = []target.Target{v}
		}
	}
	return result
}

func (tm *TargetManager) AllTargets() map[string][]target.Target {
	result := make(map[string][]target.Target, len(tm.targets))
	for k, v := range tm.targets {
		result[k] = []target.Target{v}
	}
	return result
}
