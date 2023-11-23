package queryrangebase

import (
	"context"
	"time"
)

// StepAlignMiddleware aligns the start and end of request to the step to
// improved the cacheability of the query results.
var StepAlignMiddleware = MiddlewareFunc[Request](func(next Handler[Request]) Handler[Request] {
	return stepAlign{
		next: next,
	}
})

type stepAlign struct {
	next Handler[Request]
}

func (s stepAlign) Do(ctx context.Context, r Request) (Response, error) {
	start := (r.GetStart().UnixMilli() / r.GetStep()) * r.GetStep()
	end := (r.GetEnd().UnixMilli() / r.GetStep()) * r.GetStep()
	return s.next.Do(ctx, r.WithStartEnd(time.UnixMilli(start), time.UnixMilli(end)))
}
