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

## Database

DNSLOL will write results to a MariaDB database. If you don't have one of these
handy, a
[`docker-compose.yml`](https://github.com/letsencrypt/dns-lots-of-lookups/blob/master/docker-compose.yml)
file is provided that can quickly create a MariaDB container for `dnslol` to
use.

Before trying to use the docker compose file make sure you have Docker Engine
1.10.0+ and Docker Compose 1.6.0+ installed. If you do not, you can follow
Docker's [installation instructions](https://docs.docker.com/compose/install/).

You can start the database by running:

```bash
    docker-compose up
```

This will start a `dnslotsoflookups_db_1` container running MariaDB. A `dnslol`
database user will be created with the password `dnslol`. This user will be
granted superuser privileges for the `dnslol-results` database. Port `3333` of
the host machine is mapped to the MariaDB instance in the
`dnslotsoflookups_db_1` container.

You can verify the database is running or perform manual queries using the
`mysql` command line tool (You may need to install this command on your host
machine separately if you don't already have it):

```bash
    mysql -u dnslol -pdnslol -P 3333 -h localhost --protocol=tcp dnslol-results
```

You can view the database logs by running:

```bash
    docker-compose logs db
```

**Important** - By default the `dnslol` user has a **hardcoded password** equal
to the username. Make sure your host machine has adqueate firewall policies to
prevent the DB container from being accessed externally! Never use the `dnslol`
DB container in a production setting!

## Metrics

DNSLOL exports several [Prometheus](https://prometheus.io/) metrics on the configured `-debugAddr` that can be used to monitor the performance and results of a `dnslol` run.

| Metric Name      | Metric Type   | Labels    | Description           |
| ---------------- |---------------|-----------|:----------------------|
| `lookup_results` | Counter Vec   | `result`  | Result count per query rcode or error result |
| `attempts`       | Counter       |           | Number of lookup attempts made |
| `successes`      | Counter       |           | Number of lookup successes |
| `queryTime`      | SummaryVec    | `type`    | Query duration (seconds) per type |
| `commandLine`    | GaugeVec      | `line`    | Command line invocation of the `dnslol` tool |
