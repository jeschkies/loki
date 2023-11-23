// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Mostly lifted from prometheus/web/api/v1/api.go.

package queryrangebase

import (
	"context"
	"flag"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

const day = 24 * time.Hour

// PassthroughMiddleware is a noop middleware
var PassthroughMiddleware = MiddlewareFunc(func(next Handler) Handler {
	return next
})

// Config for query_range middleware chain.
type Config struct {
	AlignQueriesWithStep bool               `yaml:"align_queries_with_step"`
	ResultsCacheConfig   ResultsCacheConfig `yaml:"results_cache"`
	CacheResults         bool               `yaml:"cache_results"`
	MaxRetries           int                `yaml:"max_retries"`
	ShardedQueries       bool               `yaml:"parallelise_shardable_queries"`
}

// RegisterFlags adds the flags required to config this to the given FlagSet.
func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	f.IntVar(&cfg.MaxRetries, "querier.max-retries-per-request", 5, "Maximum number of retries for a single request; beyond this, the downstream error is returned.")
	f.BoolVar(&cfg.AlignQueriesWithStep, "querier.align-querier-with-step", false, "Mutate incoming queries to align their start and end with their step.")
	f.BoolVar(&cfg.CacheResults, "querier.cache-results", false, "Cache query results.")
	f.BoolVar(&cfg.ShardedQueries, "querier.parallelise-shardable-queries", true, "Perform query parallelisations based on storage sharding configuration and query ASTs. This feature is supported only by the chunks storage engine.")

	cfg.ResultsCacheConfig.RegisterFlags(f)
}

// Validate validates the config.
func (cfg *Config) Validate() error {
	if cfg.CacheResults {
		if err := cfg.ResultsCacheConfig.Validate(); err != nil {
			return errors.Wrap(err, "invalid results_cache config")
		}
	}
	return nil
}

// HandlerFunc is like http.HandlerFunc, but for Handler.
type HandlerFunc[R Request] func(context.Context, R) (Response, error)

// Do implements Handler.
func (q HandlerFunc[R]) Do(ctx context.Context, req R) (Response, error) {
	return q(ctx, req)
}

// Handler is like http.Handle, but specifically for Prometheus query_range calls.
type Handler[R Request] interface {
	Do(context.Context, R) (Response, error)
}

// MiddlewareFunc is like http.HandlerFunc, but for Middleware.
type MiddlewareFunc[R Request] func(Handler[R]) Handler[R]

// Wrap implements Middleware.
func (q MiddlewareFunc[R]) Wrap(h Handler[R]) Handler[R] {
	return q(h)
}

// Middleware is a higher order Handler.
type Middleware[R Request] interface {
	Wrap(Handler[R]) Handler[R]
}

// MergeMiddlewares produces a middleware that applies multiple middleware in turn;
// ie Merge(f,g,h).Wrap(handler) == f.Wrap(g.Wrap(h.Wrap(handler)))
func MergeMiddlewares[R Request](middleware ...Middleware[R]) Middleware[R] {
	return MiddlewareFunc[R](func(next Handler[R]) Handler[R] {
		for i := len(middleware) - 1; i >= 0; i-- {
			next = middleware[i].Wrap(next)
		}
		return next
	})
}

// Tripperware is a signature for all http client-side middleware.
type Tripperware func(http.RoundTripper) http.RoundTripper

// RoundTripFunc is to http.RoundTripper what http.HandlerFunc is to http.Handler.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
