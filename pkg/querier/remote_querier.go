package querier

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/grafana/loki/pkg/iter"
	"github.com/grafana/loki/pkg/loghttp"
	"github.com/grafana/loki/pkg/logproto"
	"github.com/grafana/loki/pkg/logql"
	"github.com/grafana/loki/pkg/storage/stores/index/stats"
	"github.com/grafana/loki/pkg/util"
	"github.com/grafana/loki/pkg/util/unmarshal"
	"github.com/prometheus/common/config"
)

const (
	readPath          = "/loki/api/v1/read"
	defaultAuthHeader = "Authorization"
)

type RemoteReadClient struct {
	TLSConfig  config.TLSConfig
	Username   string
	Password   string
	Address    string
	OrgID      string
	ProxyURL   string
	AuthHeader string

	conn *websocket.Conn
}

func (c *RemoteReadClient) RemoteReadConn(queryStr string, start time.Time, quiet bool) (*websocket.Conn, error) {
	params := util.NewQueryStringBuilder()
	params.SetString("query", queryStr)
	params.SetInt("start", start.UnixNano())

	return c.wsConnect(readPath, params.Encode(), quiet)
}

func (c *RemoteReadClient) Recv() (*logproto.QueryResponse, error) {
	var err error

	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	r := &logproto.QueryResponse{}
	err = unmarshal.ReadRemoteResponseJSON(r, data)
	return r, err
}

func (c *RemoteReadClient) wsConnect(path, query string, quiet bool) (*websocket.Conn, error) {
	us, err := buildURL(c.Address, path, query)
	if err != nil {
		return nil, err
	}

	tlsConfig, err := config.NewTLSConfig(&c.TLSConfig)
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(us, "http") {
		us = strings.Replace(us, "http", "ws", 1)
	}

	if !quiet {
		log.Println(us)
	}

	h, err := c.getHTTPRequestHeader()
	if err != nil {
		return nil, err
	}

	ws := websocket.Dialer{
		TLSClientConfig: tlsConfig,
	}

	if c.ProxyURL != "" {
		ws.Proxy = func(req *http.Request) (*url.URL, error) {
			return url.Parse(c.ProxyURL)
		}
	}

	conn, resp, err := ws.Dial(us, h)
	if err != nil {
		if resp == nil {
			return nil, err
		}
		buf, _ := ioutil.ReadAll(resp.Body) // nolint
		return nil, fmt.Errorf("Error response from server: %s (%v)", string(buf), err)
	}

	return conn, nil
}

func (c *RemoteReadClient) getHTTPRequestHeader() (http.Header, error) {
	h := make(http.Header)

	if c.Username != "" && c.Password != "" {
		if c.AuthHeader == "" {
			c.AuthHeader = defaultAuthHeader
		}
		h.Set(
			c.AuthHeader,
			"Basic "+base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password)),
		)
	}

	if c.OrgID != "" {
		h.Set("X-Scope-OrgID", c.OrgID)
	}
	return h, nil
}

// buildURL concats a url `http://foo/bar` with a path `/buzz`.
func buildURL(u, p, q string) (string, error) {
	url, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	url.Path = path.Join(url.Path, p)
	url.RawQuery = q
	return url.String(), nil
}

type remoteReadClientIterator struct {
	client *RemoteReadClient
	query  string
	start  time.Time
	err    error
	curr   iter.EntryIterator
}

func NewRemoteReadClientIterator(client *RemoteReadClient, query string, start time.Time) (iter.EntryIterator, error) {
	var err error
	client.conn, err = client.RemoteReadConn(query, start, true)
	if err != nil {
		return nil, err
	}
	return &remoteReadClientIterator{
		client: client,
		query:  query,
		start:  start,
	}, nil
}

func (i *remoteReadClientIterator) Next() bool {
	for i.curr == nil || !i.curr.Next() {
		batch, err := i.client.Recv()
		if err == io.EOF {
			return false
		} else if err != nil {
			i.err = err
			return false
		}
		i.curr = iter.NewQueryResponseIterator(batch, logproto.BACKWARD)
	}

	return true
}

func (i *remoteReadClientIterator) Entry() logproto.Entry {
	return i.curr.Entry()
}

func (i *remoteReadClientIterator) Labels() string {
	return i.curr.Labels()
}

func (i *remoteReadClientIterator) StreamHash() uint64 { return i.curr.StreamHash() }

func (i *remoteReadClientIterator) Error() error {
	return i.err
}

func (i *remoteReadClientIterator) Close() error {
	return i.client.conn.Close()
}

type RemoteClusterConfig struct {
	Address string
	OrgID   string
}

type RemoteReadConfig struct {
	Clusters []RemoteClusterConfig
}

func (c RemoteReadConfig) IsDefined() bool {
	return len(c.Clusters) > 0
}

type RemoteReadQuerier struct {
	remoteClusters []*RemoteReadClient
}

func NewRemoteReadQuerier(cfg RemoteReadConfig) *RemoteReadQuerier {
	remoteClusters := make([]*RemoteReadClient, 0, len(cfg.Clusters))
	for _, cluster := range cfg.Clusters {
		remoteClusters = append(remoteClusters, &RemoteReadClient{
			TLSConfig: config.TLSConfig{},
			Address:   cluster.Address,
			OrgID:     cluster.OrgID, // TODO: support multiple tenants.
		})
	}

	return &RemoteReadQuerier{
		remoteClusters: remoteClusters,
	}
}

func (q *RemoteReadQuerier) SelectLogs(ctx context.Context, params logql.SelectLogParams) (iter.EntryIterator, error) {

	iters := make([]iter.EntryIterator, len(q.remoteClusters))
	i := 0
	var err error
	for _, remoteCluster := range q.remoteClusters {
		iters[i], err = NewRemoteReadClientIterator(remoteCluster, params.Selector, params.GetStart())
		if err != nil {
			return nil, err
		}
		i++
	}

	return iter.NewSortEntryIterator(iters, params.Direction), nil
}

func (q *RemoteReadQuerier) SelectSamples(ctx context.Context, params logql.SelectSampleParams) (iter.SampleIterator, error) {
	return nil, nil
}

func (q *RemoteReadQuerier) Label(ctx context.Context, req *logproto.LabelRequest) (*logproto.LabelResponse, error) {
	return nil, nil
}

func (q *RemoteReadQuerier) Series(ctx context.Context, req *logproto.SeriesRequest) (*logproto.SeriesResponse, error) {
	return nil, nil
}

func (q *RemoteReadQuerier) Tail(ctx context.Context, req *logproto.TailRequest) (*Tailer, error) {
	return nil, nil
}

func (q *RemoteReadQuerier) IndexStats(ctx context.Context, req *loghttp.RangeQuery) (*stats.Stats, error) {
	return nil, nil
}
