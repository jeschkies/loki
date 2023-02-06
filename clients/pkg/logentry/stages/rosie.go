package stages

import (
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/mitchellh/mapstructure"
	"github.com/prometheus/common/model"
	rosie "github.com/jeschkies/rosie-go/pkg"
)

type RosieConfig struct {
	Expression string  `mapstructure:"expression"`
}

type rosieStage struct {
	cfg        *RosieConfig
	engine     *rosie.Engine
	pattern    *rosie.Pattern
	logger     log.Logger
}

func newRosieStage(logger log.Logger, config interface{}) (Stage, error) {
	cfg, err := parseRosieConfig(config)
	if err != nil {
		return nil, err
	}

	engine, err := rosie.New("rosie")
	if err != nil {
		return nil, err
	}

	stage := &rosieStage{
		cfg:    cfg,
		engine: engine,
		logger: log.With(logger, "component", "stage", "type", "rosie"),
	}

	pat, _, err := engine.Compile(cfg.Expression)
	if err != nil {
		return nil, err
	}
	return toStage(stage), nil
}

func parseRosieConfig(config interface{}) (*RosieConfig, error) {
	cfg := &RosieConfig{}
	err := mapstructure.Decode(config, cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Process implements Stage
func (r *rosieStage) Process(labels model.LabelSet, extracted map[string]interface{}, t *time.Time, entry *string) {
	input := entry

	match := r.pattern.MatchString(*input)
	if match == nil {
		if Debug {
			level.Debug(r.logger).Log("msg", "rosie did not match", "input", *input)
		}
		return
	}

	for name, m := range match.Data {
		extracted[name] = string(m)
	}
}

// Name implements Stage
func (r *rosieStage) Name() string {
	return StageTypeRosie
}
