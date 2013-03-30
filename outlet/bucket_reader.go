package outlet

import (
	"fmt"
	"l2met/bucket"
	"l2met/store"
	//"l2met/utils"
	"time"
)

type BucketReader struct {
	Store     store.Store
	Interval  time.Duration
	Partition string
	Ttl       uint64
	NumOutlets int
	NumScanners int
	Inbox     chan *bucket.Bucket
	Outbox    chan *bucket.Bucket
}

func NewBucketReader() *BucketReader {
	rdr := new(BucketReader)
	rdr.Inbox = make(chan *bucket.Bucket, 10000)
	return rdr
}

func (r *BucketReader) Start(out chan *bucket.Bucket) {
	r.Outbox = out
	go r.scan()
	for i := 0; i < r.NumOutlets; i++ {
		go r.outlet()
	}
}

func (r *BucketReader) scan() {
	for _ = range time.Tick(r.Interval) {
		/*
		p, err := utils.LockPartition(r.Partition, r.Store.MaxPartitions(), r.Ttl)
		if err != nil {
			continue
		}
		*/
		partition := fmt.Sprintf("outlet.%d", 0)
		for bucket := range r.Store.Scan(partition) {
			r.Inbox <- bucket
		}
		//utils.UnlockPartition(partition)
	}
}
func (r *BucketReader) outlet() {
	for b := range r.Inbox {
		r.Store.Get(b)
		r.Outbox <- b
	}
}
