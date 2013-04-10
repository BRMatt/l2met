# l2met

Convert a formatted log stream into metrics.

* [Synopsis](#synopsis)
* [Features](#features)
* [Log Conventions](#log-conventions)
* [Setup](#setup)

## Synopsis

L2met receives HTTP requests that contain a body of rfc5424 formatted data. Commonly data is drained into l2met by [logplex](https://github.com/heroku/logplex) or [log-shuttle](https://github.com/ryandotsmith/log-shuttle).

Once data is delivered, l2met extracts and parses the individual log lines using the [log conventions](#log-conventions) and then stores the data in redis so that outlets can read the data and build metrics. The librato_outlet is the most popular and will put all of your metrics into your librato account. See the [setup](#setup) section to get started.

## Log Conventions

L2met uses convention over configuration to build metrics. Keys that are prefixed with measre and have numerical values will be analyzed. For example:

```ruby
$stdout.puts("measure.db.latency=20")
```
Metrics Produced:

* db.latency.min
* db.latency.median
* db.latency.perc95
* db.latency.perc99
* db.latency.max
* db.latency.mean
* db.latency.last
* db.latency.count
* db.latency.sum

## Features

* [High resolution buckets](#high-resolution-buckets)
* [Drain Prefix](#drain-prefix)
* [Multi-metrics](#multi-metrics)
* [Heroku Router](#heroku-router)
* [Bucket attrs](#bucket-attrs)
* Librato Outlet
* Graphite Outlet

### Multi-metrics

We want to be able to specify multiple measurements on a single line so as not to have to pay the (albeit low) overhead of writing to stdout. However, we don't want to take every k=v under the sun. L2met has always forced you to think about the things your are measuring and this feature does not regress in that regard.

Example:

```
echo 'measure=hello val=10 measure.world=10' | log-shuttle
```

This will result in 2 buckets:

1. {name=hello, vals=[10], ...}
2. {name=world, vals=[10], ...}

Thus you can measure multiple things provided the key is prefixed with `measure.`.

### Heroku Router

The Heroku router has a log line convention described [here](https://devcenter.heroku.com/articles/http-routing#heroku-router-log-format)

This feature will read the User field in the syslog packet looking for the string "router." If a match is had, we will parse the structured data and massage it to match the l2met convention. Furthermore, we will prefix the measurement name with the parsed host field in the log line.

Example:

```
path=/logs host=test.l2met.net connect=1ms service=2ms bytes=0
```

Would produce the following buckets:

1. {name=router.connect source=test.l2met.net.connect vals=[1]}
2. {name=router.service source=test.l2met.net.service vals=[2]}
3. {name=router.bytes   source=test.l2met.net.bytes   vals=[0]}

### Drain Parameters

There are several configuration options that can be specified at the drain level. These options will be applied to all of the metrics coming into the drain.

* Resolution
* Prefix

#### High Resolution Buckets

By default, l2met will hold measurements in 1 minute buckets. This means that data visible in librato and graphite have a granularity of 1 minute. However, It is possible to to achieve a greater level of resolution. For example, you can get 1 second level resolution by appending a query parameter to your drain url. Notice that the resolution is specified in seconds.

The supported resolutions are: 1, 5, 30, 60.

Drain URL:

```
https://user:token@l2met.herokuapp.com/logs?resolution=1
```

#### Drain Prefix

It can be useful to prepend a string to all metrics going into a drain. For instance, say you want all of your pretrics to include the environment or app name. You can acheive this by adding a drain prefix.

Drain URL:

```
https://token@l2met.herokuapp.com/logs?prefix=myapp.staging
```

### Bucket Attrs

Most outlet providers allow meta-data to be associated with measurements. Bucket attrs is the l2met feature that allows you to interact with this meta-data.

#### Units

L2met supports associating units with numbers by appending on a non-digit sequence after the digits. L2met will assign all measurements a units of *y* unless specified. You can specify a unit by prepending `/[a-zA-z]+/` after the digit. For instance:

```
measure.db.get=1ms measure.web.get=1
```

This will create the following buckets:

1. {name="measure.db.get", vals=[1], units="ms"}
2. {name="measure.web.get", vals=[1], units="y"}

#### Min Y Value

Librato charts allow charts to have a min y value. L2met sets this to 0. It cant not be overriden at this time. Open a GH issue if this is a problem for you.


## Setup

You can run l2met in a multi-tenant mode or a single-user mode. The multi-tenant mode enables multiple drains with unique librato accounts. The single-user mode exposes 1 drain and maps to 1 librato account. This setup guide assumes single-user mode.

#### Create Librato Account

Once you have created the account, visit your [settings](https://metrics.librato.com/account) page to grab you username and token. Keep this page open as you will need this data later in the setup.

#### Create a Heroku app.

```bash
$ git clone git://github.com/ryandotsmith/l2met.git
$ cd l2met
$ heroku create your-l2met --buildpack git://github.com/kr/heroku-buildpack-go.git
$ git push heroku master
```

#### Add database services.

I prefer redisgreen, however there are cheaper alternatives. Whichever provider you choose, ensure that you have set REDIS_URL properly.

```bash
$ heroku addons:add redisgreen:basic
$ heroku config:set REDIS_URL=$(heroku config -s | grep "^REDISGREEN_URL" | sed 's/REDISGREEN_URL=//')
```

#### Update Heroku config.

```bash
$ heroku config:set APP_NAME=your-l2met LIBRATO_USER=u@d.com LIBRATO_TOKEN=abc
```

#### Scale processes.

```bash
$ heroku scale web=1 librato_outlet=1
```

If you wish to run more than 1 outlet, you will need to adjust a config variable in addition to scaling the process. For example, if you are running 2 librato_outlets:

```bash
$ heroku config:set NUM_OUTLET_PARTITIONS=2
$ heroku scale librato_outlet=2
```

#### Add drain to your Heroku app(s)

Now that you have created an l2met app, you can drain logs from other heroku apps into l2met.

```bash
$ heroku drains:add https://l2met:`uuid`@your-l2met.herokuapp.com/logs -a your-app-that-needs-l2met
```

#### Test

Install [log-shuttle](https://github.com/ryandotsmith/log-shuttle).

```bash
$ curl -o log-shuttle "https://s3.amazonaws.com/32k.io/bin/log-shuttle"
$ chmod +x log-shuttle
```

Send data to l2met.

```bash
$ export LOGPLEX_URL=https://l2met:123@your-l2met.herokuapp.com/logs
$ echo 'measure="hello-from-your-l2met"' | log-shuttle
```

Now you should be able to see `hello-from-your-l2met` in your librato metrics web ui.
