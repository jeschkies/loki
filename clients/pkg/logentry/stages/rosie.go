package stages

import (
	"fmt"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	rosie "github.com/jeschkies/rosie-go/pkg"
	"github.com/mitchellh/mapstructure"
	"github.com/prometheus/common/model"
)

type RosieConfig struct {
	Expression string `mapstructure:"expression"`
}

type rosieStage struct {
	cfg     *RosieConfig
	engine  *rosie.Engine
	pattern *rosie.Pattern
	logger  log.Logger
}

func newRosieStage(logger log.Logger, config interface{}) (Stage, error) {
	cfg, err := parseRosieConfig(config)
	if err != nil {
		return nil, err
	}

	engine, err := rosie.New("rosie")
	engine.ImportPkg("net")
	engine.ImportPkg("word")
	if err != nil {
		return nil, err
	}

	stage := &rosieStage{
		cfg:    cfg,
		engine: engine,
		logger: log.With(logger, "component", "stage", "type", "rosie"),
	}

	pat, messages, err := engine.Compile(cfg.Expression)
	for _, msg := range messages {
		level.Info(stage.logger).Log("msg", fmt.Sprintf("%v", msg))
	}
	if err != nil {
		return nil, err
	}
	stage.pattern = pat
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

	match, _ := r.pattern.MatchString(*input)
	if match == nil {
		if Debug {
			level.Debug(r.logger).Log("msg", "rosie did not match", "input", *input)
		}
		return
	}

	for name, m := range match.Data {
		extracted[name] = fmt.Sprintf("%v", m)
	}
}

// Name implements Stage
func (r *rosieStage) Name() string {
	return StageTypeRosie
}
