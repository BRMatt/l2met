package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"l2met/store"
	"l2met/utils"
	"net/http"
	"strconv"
	"time"
	"net"
)

var (
	workers         = flag.Int("workers", 4, "Number of routines that will post data to librato")
	processInterval = flag.Int("proc-int", 5, "Number of seconds to wait in between bucket processing.")
)

func init() {
	flag.Parse()
	http.DefaultTransport = &http.Transport{
		Dial: func(n, a string) (net.Conn, error) {
			c, err := net.DialTimeout(n, a, time.Second * 2)
			if err != nil {
				return c, err
			}
			return c, c.SetDeadline(time.Now().Add(time.Second * 5))
		},
	}
}

type LM struct {
	Name   string `json:"name"`
	Time   int64  `json:"measure_time"`
	Val    string `json:"value"`
	Source string `json:"source,omitempty"`
	Token  string
}

type LP struct {
	Gauges *[]*LM `json:"gauges"`
}

var (
	libratoUrl = "https://metrics-api.librato.com/v1/metrics"
)

func main() {
	// The inbox is used to hold empty buckets that are
	// waiting to be processed. We buffer the chanel so
	// as not to slow down the fetch routine. We can
	inbox := make(chan *store.Bucket, 1000)
	// The converter will take items from the inbox,
	// fill in the bucket with the vals, then convert the
	// bucket into a librato metric.
	lms := make(chan *LM, 1000)
	// The converter will place the librato metrics into
	// the outbox for HTTP submission. We rely on the batch
	// routine to make sure that the collections of librato metrics
	// in the outbox are homogeneous with respect to their token.
	// This ensures that we route the metrics to the correct librato account.
	outbox := make(chan *[]*LM, 1000)

	// Routine that reads ints from the database
	// and sends them to the inbox.
	go scheduleFetch(inbox)

	// We take the empty buckets from the inbox,
	// get the values from the database, then make librato metrics out of them.
	for i := 0; i < *workers; i++ {
		go scheduleConvert(inbox, lms)
	}

	// Shouldn't need to be concurrent since it's responsibility
	// it to serialize a collection of librato metrics.
	go batch(lms, outbox)

	// These routines involve reading data from the database
	// and making HTTP requests. We will want to take advantage of
	// parallel processing.
	for i := 0; i < *workers; i++ {
		go schedulePost(outbox)
	}

	// Print chanel metrics & live forever.
	report(inbox, lms, outbox)
}

func report(i chan *store.Bucket, l chan *LM, o chan *[]*LM) {
	for _ = range time.Tick(time.Second * 5) {
		utils.MeasureI("librato.inbox", int64(len(i)))
		utils.MeasureI("librato.lms", int64(len(l)))
		utils.MeasureI("librato.outbox", int64(len(o)))
	}
}

// Fetch should kick off the librato outlet process.
// Its responsibility is to get the ids of buckets for the current time,
// make empty Buckets, then place the buckets in an inbox to be filled
// (load the vals into the bucket) and processed.
func scheduleFetch(inbox chan<- *store.Bucket) {
	for t := range time.Tick(time.Second) {
		if t.Second()%*processInterval == 0 {
			fetch(t, inbox)
		}
	}
}

func fetch(t time.Time, inbox chan<- *store.Bucket) {
	fmt.Printf("at=start_fetch minute=%d\n", t.Minute())
	defer utils.MeasureT(time.Now(), "librato.fetch")
	max := utils.RoundTime(t, time.Minute)
	min := max.Add(-time.Minute)
	ids, err := scanBuckets(min, max)
	if err != nil {
		return
	}
	for i := range ids {
		inbox <- &store.Bucket{Id: ids[i]}
	}
}

func scanBuckets(min, max time.Time) ([]int64, error) {
	defer utils.MeasureT(time.Now(), "librato.scan-buckets")
	rows, err := pg.Query("select id from metrics where bucket >= $1 and bucket < $2 order by bucket desc",
		min, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var buckets []int64
	for rows.Next() {
		var id int64
		err = rows.Scan(&id)
		if err == nil {
			buckets = append(buckets, id)
		}
	}
	return buckets, nil
}

func scheduleConvert(inbox <-chan *store.Bucket, lms chan<- *LM) {
	for b := range inbox {
		err := b.Get()
		if err != nil {
			fmt.Printf("error=%s\n", err)
			continue
		}
		if len(b.Vals) == 0 {
			fmt.Printf("at=bucket-no-vals name=%s\n", b.Name)
			continue
		}
		fmt.Printf("at=librato.process.bucket minute=%d name=%q\n",
			b.Time.Minute(), b.Name)
		convert(b, lms)
	}
}

func convert(b *store.Bucket, lms chan<- *LM) {
	defer utils.MeasureT(time.Now(), "librato.convert")
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".last", Val: ff(b.Last())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".min", Val: ff(b.Min())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".max", Val: ff(b.Max())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".mean", Val: ff(b.Mean())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".median", Val: ff(b.Median())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".perc95", Val: ff(b.P95())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".perc99", Val: ff(b.P99())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".count", Val: fi(b.Count())}
	lms <- &LM{Token: b.Token, Time: b.Time.Unix(), Source: b.Source, Name: b.Name + ".sum", Val: ff(b.Sum())}
}

func ff(x float64) string {
	return strconv.FormatFloat(x, 'f', 5, 64)
}

func fi(x int) string {
	return strconv.FormatInt(int64(x), 10)
}

func batch(lms <-chan *LM, outbox chan<- *[]*LM) {
	ticker := time.Tick(time.Second)
	batchMap := make(map[string]*[]*LM)
	for {
		select {
		case <-ticker:
			for k, v := range batchMap {
				if len(*v) > 0 {
					outbox <- v
					delete(batchMap, k)
				}
			}
		case lm := <-lms:
			k := lm.Token
			v, ok := batchMap[k]
			if !ok {
				tmp := make([]*LM, 0, 50)
				v = &tmp
				batchMap[k] = v
			}
			*v = append(*v, lm)
			if len(*v) == cap(*v) {
				outbox <- v
				delete(batchMap, k)
			}
		}
	}
}

func schedulePost(outbox <-chan *[]*LM) {
	for metrics := range outbox {
		if len(*metrics) < 1 {
			fmt.Printf("at=%q\n", "post.empty.metrics")
			continue
		}
		go func() {
			err := post(metrics)
			if err != nil {
				fmt.Printf("at=post-error error=%s\n", err)
			}
		}()
	}
}

func post(metrics *[]*LM) error {
	sampleMetric := *(*metrics)[0]
	token := store.Token{Id: sampleMetric.Token}
	token.Get()

	payload := LP{metrics}
	j, err := json.Marshal(payload)
	postBody := bytes.NewBuffer(j)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", libratoUrl, postBody)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")
	req.SetBasicAuth(token.User, token.Pass)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := ioutil.ReadAll(resp.Body)
		fmt.Printf("status=%d post-body=%s resp-body=%s\n",
			resp.StatusCode, postBody, b)
	}
	return nil
}
