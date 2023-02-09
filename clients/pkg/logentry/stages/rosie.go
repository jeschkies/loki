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

type MatchData struct {
	Subs []Sub
}

type Sub struct {
	Data string
	Type string
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
	_, _, _, err = engine.ImportPkg("net")
	if err != nil {
		return nil, err
	}
	_, _, _, err = engine.ImportPkg("word")
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

	matches := &MatchData{}
	mapstructure.Decode(match.Data, matches)
	for _, sub := range matches.Subs {
		extracted[sub.Type] = sub.Data
	}
}

// Name implements Stage
func (r *rosieStage) Name() string {
	return StageTypeRosie
}
