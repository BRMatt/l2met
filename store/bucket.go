package store

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/bmizerany/logplex"
	"github.com/garyburd/redigo/redis"
	"hash/crc64"
	"io"
	"l2met/encoding"
	"l2met/utils"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	keySep = "→"
)

var (
	maxPartitions uint64
)

func init() {
	var err error
	tmp := os.Getenv("MAX_LIBRATO_PROCS")
	maxPartitions, err = strconv.ParseUint(tmp, 10, 64)
	if err != nil {
		fmt.Printf("error=%q err=%s\n", "Unable to read MAX_LIBRATO_PROCS.", err)
		os.Exit(1)
	}
}

type BKey struct {
	Token  string
	Name   string
	Source string
	Time   time.Time
}

// time:token:name:source
func ParseKey(s string) (*BKey, error) {
	parts := strings.Split(s, keySep)
	if len(parts) < 3 {
		return nil, errors.New("bucket: Unable to parse bucket key.")
	}

	t, err := strconv.ParseInt(parts[0], 10, 54)
	if err != nil {
		return nil, err
	}

	time := time.Unix(t, 0)
	if err != nil {
		return nil, err
	}

	key := new(BKey)
	key.Time = time
	key.Token = parts[1]
	key.Name = parts[2]
	if len(parts) > 3 {
		key.Source = parts[3]
	}
	return key, nil
}

type Bucket struct {
	sync.Mutex
	Key  BKey
	Vals []float64 `json:"vals,omitempty"`
}

func NewBucket(token string, rdr *bufio.Reader) <-chan *Bucket {
	buckets := make(chan *Bucket, 10)
	go func(c chan<- *Bucket) {
		defer close(c)
		lp := logplex.NewReader(rdr)
		for {
			packet, err := lp.ReadMsg()
			if err != nil {
				if err == io.EOF {
					break
				}
				fmt.Printf("at=logplex-error err=%s\n", err)
				return
			}
			utils.Measure("received-log-line")
			utils.Measure(token + "-received-log-line")
			d, err := encoding.ParseMsgData(packet.Msg)
			if err != nil {
				continue
			}

			name, ok := d["measure"]
			if !ok {
				continue
			}

			source, ok := d["source"]
			if !ok {
				source = ""
			}

			var val float64
			tmpVal, ok := d["val"]
			if ok {
				val, err = strconv.ParseFloat(tmpVal, 64)
				if err != nil {
					fmt.Printf("at=error error=\"unable to parse val.\"\n")
					continue
				}
			} else {
				val = float64(1)
			}

			t, err := packet.Time()
			if err != nil {
				fmt.Printf("at=time-error error=%s\n", err)
				continue
			}
			t = utils.RoundTime(t, time.Minute)

			k := BKey{Token: token, Name: name, Source: source, Time: t}
			b := &Bucket{Key: k}
			b.Vals = append(b.Vals, val)
			c <- b
			utils.Measure(token + "-received-measurement")
			utils.Measure("received-measurement")
		}
	}(buckets)
	return buckets
}

func (b *Bucket) Add(otherM *Bucket) {
	b.Lock()
	defer b.Unlock()
	for _, v := range otherM.Vals {
		b.Vals = append(b.Vals, v)
	}
}

func (b *Bucket) Partition() string {
	defer utils.MeasureT(time.Now(), "compute-partition")
	tab := crc64.MakeTable(crc64.ISO)
	check := crc64.Checksum([]byte(b.String()), tab)
	partition := check % maxPartitions
	return "partition:" + strconv.FormatUint(partition, 10)
}

// time:token:name:source
func (b *Bucket) String() (res string) {
	res += strconv.FormatInt(b.Key.Time.Unix(), 10) + keySep
	res += b.Key.Token + keySep
	res += b.Key.Name
	if len(b.Key.Source) > 0 {
		res += keySep + b.Key.Source
	}
	return
}

func (b *Bucket) Get() error {
	defer utils.MeasureT(time.Now(), "bucket.get")

	rc := redisPool.Get()
	defer rc.Close()

	//Fill in the vals.
	reply, err := redis.Values(rc.Do("LRANGE", b.String(), 0, -1))
	if err != nil {
		return err
	}
	for _, item := range reply {
		v, ok := item.([]byte)
		if !ok {
			continue
		}
		err = encoding.DecodeArray(v, &b.Vals)
	}
	return nil
}

func (b *Bucket) Put() error {
	defer utils.MeasureT(time.Now(), "bucket.put")
	startLock := time.Now()
	b.Lock()
	inLock := time.Now()
	t := time.Since(startLock) / time.Millisecond
	utils.MeasureI("bucket-lock-acquired", int64(t))
	vals := b.Vals
	partition := b.Partition()
	key := b.String()
	b.Unlock()
	t = time.Since(inLock) / time.Millisecond
	utils.MeasureI("bucket-lock-released", int64(t))

	defer utils.MeasureT(time.Now(), "redis.push")
	rc := redisPool.Get()
	defer rc.Close()
	rc.Send("MULTI")
	rc.Send("RPUSH", key, vals)
	rc.Send("EXPIRE", key, 300)
	rc.Send("SADD", partition, key)
	rc.Send("EXPIRE", partition, 300)
	_, err := rc.Do("EXEC")
	if err != nil {
		return err
	}
	return nil
}

func (b *Bucket) Count() int {
	return len(b.Vals)
}

func (b *Bucket) Sum() float64 {
	s := float64(0)
	for i := range b.Vals {
		s += b.Vals[i]
	}
	return s
}

func (b *Bucket) Mean() float64 {
	return b.Sum() / float64(b.Count())
}

func (b *Bucket) Sort() {
	if !sort.Float64sAreSorted(b.Vals) {
		sort.Float64s(b.Vals)
	}
}

func (b *Bucket) Min() float64 {
	b.Sort()
	return b.Vals[0]
}

func (b *Bucket) Median() float64 {
	b.Sort()
	pos := int(math.Ceil(float64(b.Count() / 2)))
	return b.Vals[pos]
}

func (b *Bucket) P95() float64 {
	b.Sort()
	pos := int(math.Floor(float64(b.Count()) * 0.95))
	return b.Vals[pos]
}

func (b *Bucket) P99() float64 {
	b.Sort()
	pos := int(math.Floor(float64(b.Count()) * 0.99))
	return b.Vals[pos]
}

func (b *Bucket) Max() float64 {
	b.Sort()
	pos := b.Count() - 1
	return b.Vals[pos]
}

func (b *Bucket) Last() float64 {
	pos := b.Count() - 1
	return b.Vals[pos]
}
