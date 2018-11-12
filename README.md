# DNS Lots-of-Lookups (DNSLOL)

[![Build Status](https://travis-ci.org/letsencrypt/dns-lots-of-lookups.svg?branch=master)](https://travis-ci.org/letsencrypt/dns-lots-of-lookups)
[![GolangCI](https://golangci.com/badges/github.com/letsencrypt/dns-lots-of-lookups.svg)](https://golangci.com/r/github.com/letsencrypt/dns-lots-of-lookups)


DNS Lots-of-Lookups (DNSLOL) is a small command line utility suitable for doing
large-scale DNS lookups quickly. In particular DNSLOL is tailored towards doing
comparative analysis of the results of doing these lookups with different DNS
servers.

## Install

DNSLOL is packaged as a [Go module](https://github.com/golang/go/wiki/Modules),
allowing you to clone the source outside of your `$GOPATH` and install using the
vendored dependencies.

1. [Set up Go 1.11+](https://golang.org/doc/install) and your `$GOPATH`
1. `git clone github.com/letsencrypt/dns-lots-of-lookups`
1. `cd dns-lots-of-lookups`
1. `go -mod=vendor install ./...`

## Usage

1. Increase the `ulimit` for the number of open files for your session to match
   your desired `-parallel` setting. E.g. for a `-parallel of 4000` run:

```bash
   ulimit nofile 4000
```

1. Start the lookup process. In this example we configure `dnslol` to perform `A`
   and `TXT` queries for every domain in `input_domains.txt`, using two local
   recursive resolvers (one on port `1053`, and one on `1054`). It will
   gradually spawn new goroutines every `15s`, up to a maximum of `4000`
   goroutines. Prometheus metrics will be exported on the metrics address,
   `http://127.0.0.1:6363/metrics`.

```bash
   dnslol \
    -checkA \
    -checkTXT \
    -servers 127.0.0.1:1053,127.0.0.1:1054 \
    -parallel 4000 \
    -spawnInterval 15s \
    -metricsAddr 127.0.0.1:6363 \
    < input_domains.txt
```

## Input

`dnslol` expects to read fully qualified domain names as input to standard in.
All of the domain names must be in ASCII form. Internationalized Domain Names
(IDN) must be converted to ASCII before using `dnslol` according to the
[IDNA2008 encoding method](http://unicode.org/reports/tr46/#ToASCII). You may
find using [this small Go
tool](https://gist.github.com/cpu/9e232491edb5fd7db18c2e1926ee532c) helpful for
converting IDN domains to ASCII:

```bash
   cat raw_input_domains.txt | go run idna-encode.go > input_domains.txt
```

`dnslol` can read input domains in label-wise reversed form if you provide the
`-reverse` label. This will automatically convert inputs like
`org.letsencrypt.www` to `www.letsencrypt.org`.

## Database

DNSLOL will write results to a MariaDB database. If you don't have one of these
handy,
a [`docker-compose.yml`](https://github.com/letsencrypt/dns-lots-of-lookups/blob/master/docker-compose.yml)
file is provided that can quickly create a MariaDB container for `dnslol` to
use.

If you do have an existing DB handy you will need to change the `-db` flag
provided to `dnslol` and ensure the database has been initialized with the SQL
found in `db-schema.sql`

Otherwise, before trying to use the docker compose file make sure you have
Docker Engine 1.10.0+ and Docker Compose 1.6.0+ installed. If you do not, you
can follow Docker's [installation
instructions](https://docs.docker.com/compose/install/).

You can start the database by running:

```bash
    docker-compose up
```

This will start a `dnslotsoflookups_db_1` container running MariaDB. A `dnslol`
database user will be created with the password `dnslol`. This user will be
granted superuser privileges for the `dnslol-results` database. The database 
will be listening on the private IP `10.10.10.2` on port `3306`.

You will need to create the initial database structure the first time you start
the database container using the `mysql` command line tool (You may need to
install this command on your host machine separately if you don't already have it):

```bash
    mysql \
      -u dnslol \
      -pdnslol \
      -P 3306 \
      -h 10.10.10.2 \
      --protocol=tcp \
      dnslol-results < db-schema.sql
```

You can view the database logs by running:

```bash
    docker-compose logs db
```

**Important** - By default the `dnslol` user has a **hardcoded password** equal
to the username. Never use the `dnslol` DB container in a production setting!

## Metrics

DNSLOL exports several [Prometheus](https://prometheus.io/) metrics on the configured `-debugAddr` that can be used to monitor the performance and results of a `dnslol` run.

| Metric Name      | Metric Type   | Labels              | Description                                  |
| ---------------- |---------------|---------------------|:---------------------------------------------|
| `lookup_results` | Counter Vec   | `server`, `result`  | Result count per query rcode or error result |
| `attempts`       | Counter       | `server`            | Number of lookup attempts made               |
| `successes`      | Counter       | `server`            | Number of lookup successes                   |
| `queryTime`      | SummaryVec    | `server`, `type`    | Query duration (seconds) per type            |
| `commandLine`    | GaugeVec      | `server`, `line`    | Command line invocation of the `dnslol` tool |
