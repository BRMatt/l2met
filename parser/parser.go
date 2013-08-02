// The parser is responsible for reading the body
// of the HTTP request and returning buckets of data.
package parser

import (
	"bufio"
	"fmt"
	"github.com/bmizerany/lpx"
	"github.com/ryandotsmith/l2met/bucket"
	"strconv"
	"strings"
	"time"
)

type options map[string][]string

var (
	routerPrefix  = "router"
	measurePrefix = "measure."
)

type parser struct {
	out  chan *bucket.Bucket
	lr   *lpx.Reader
	ld   *logData
	opts options
}

func BuildBuckets(body *bufio.Reader, opts options) <-chan *bucket.Bucket {
	p := new(parser)
	p.opts = opts
	p.out = make(chan *bucket.Bucket)
	p.lr = lpx.NewReader(body)
	p.ld = NewLogData()
	go p.parse()
	return p.out
}

func (p *parser) parse() {
	defer close(p.out)
	for p.lr.Next() {
		p.ld.Reset()
		if err := p.ld.Read(p.lr.Bytes()); err != nil {
			fmt.Printf("error=%s\n", err)
			continue
		}
		for _, t := range p.ld.Tuples {
			p.handleHkRouter(t)
			p.handlMeasurements(t)
		}
	}
}

func (p *parser) handlMeasurements(t *tuple) error {
	if !strings.HasPrefix(t.Name(), measurePrefix) {
		return nil
	}
	id := new(bucket.Id)
	id.Resolution = p.Resolution()
	id.Time = p.Time()
	id.User = p.User()
	id.Pass = p.Pass()
	id.Name = p.Prefix(t.Name())
	id.Units = t.Units()
	id.Source = p.ld.Source()
	val, err := t.Float64()
	if err != nil {
		return err
	}
	p.out <- &bucket.Bucket{
		Id: id,
		Vals: []float64{val},
		Emtr: bucket.MeasureEmitter,
	}
	return nil
}

func (p *parser) handleHkRouter(t *tuple) error {
	if string(p.lr.Header().Procid) != routerPrefix {
		return nil
	}
	id := new(bucket.Id)
	id.Resolution = p.Resolution()
	id.Time = p.Time()
	id.User = p.User()
	id.Pass = p.Pass()
	id.Source = p.ld.Source()
	id.Units = t.Units()
	switch t.Name() {
	case "bytes":
		id.Name = p.Prefix("router.bytes")
	case "connect":
		id.Name = p.Prefix("router.connect")
	case "service":
		id.Name = p.Prefix("router.service")
	default:
		return nil
	}
	val, err := t.Float64()
	if err != nil {
		return err
	}
	p.out <- &bucket.Bucket{
		Id: id,
		Vals: []float64{val},
		Emtr: bucket.MeasureEmitter,
	}
	return nil
}

func (p *parser) Prefix(suffix string) string {
	//Remove measure. from the name if present.
	if strings.HasPrefix(suffix, measurePrefix) {
		suffix = suffix[len(measurePrefix):]
	}
	pre, present := p.opts["prefix"]
	if !present {
		return suffix
	}
	return pre[0] + "." + suffix
}

func (p *parser) User() string {
	return p.opts["user"][0]
}

func (p *parser) Pass() string {
	return p.opts["password"][0]
}

func (p *parser) Time() time.Time {
	ts := string(p.lr.Header().Time)
	d := p.Resolution()
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t = time.Now()
	}
	return time.Unix(0, int64((time.Duration(t.UnixNano())/d)*d))
}

func (p *parser) Resolution() time.Duration {
	resTmp, present := p.opts["resolution"]
	if !present {
		resTmp = []string{"60"}
	}

	res, err := strconv.Atoi(resTmp[0])
	if err != nil {
		return time.Minute
	}

	return time.Second * time.Duration(res)
}
