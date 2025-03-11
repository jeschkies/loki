package log

import (
	"sync"
)

type stringMapPool struct {
	pool sync.Pool
}

func newStringMapPool() *stringMapPool {
	return &stringMapPool{
		pool: sync.Pool{
			New: func() interface{} {
				return make(map[string]string)
			},
		},
	}
}

func (s *stringMapPool) Get() map[string]string {
	m := s.pool.Get().(map[string]string)
	clear(m)
	return m
}

func (s *stringMapPool) Put(m map[string]string) {
	s.pool.Put(m)
}

var smp = newStringMapPool()
