package main

import (
	"flag"
	"fmt"
	"github.com/garyburd/redigo/redis"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type conf struct {
	GraphiteHost string
	RedisHost    string
	Interval     string
	Prefix       string
	Daemon       bool
	interval     time.Duration
}

func (c *conf) setFlags() {
	defaultGraphite := "localhost:2003"
	defaultRedis := "localhost:6379"
	defaultInterval := "10s"
	defaultPrefix := "redis.$host"
	if e := os.Getenv("GRAPHITE_HOST"); e != "" {
		defaultGraphite = e
	}
	if e := os.Getenv("REDIS_HOST"); e != "" {
		defaultRedis = e
	}
	if e := os.Getenv("MONITOR_INTERVAL"); e != "" {
		defaultInterval = e
	}
	if e := os.Getenv("MONITOR_PREFIX"); e != "" {
		defaultPrefix = e
	}

	flag.StringVar(&(c.GraphiteHost), "graphite", defaultGraphite, "Grahite Host Url (host:port)")
	flag.StringVar(&(c.RedisHost), "redis", defaultRedis, "Redis Instance (host:port,host:port,host:port...)")
	flag.StringVar(&(c.Interval), "interval", defaultInterval, "Metrics send interval (only valid if daemon = true)")
	flag.StringVar(&(c.Prefix), "prefix", defaultPrefix, "Metrics prefix")
	flag.BoolVar(&(c.Daemon), "daemon", false, "Daemonize and report every [interval] seconds")
}

func (c *conf) GetPrefix(Host string) string {
	prefix := c.Prefix
	host := strings.Replace(Host, ".", "_", -1)
	prefix = strings.Replace(prefix, "$host", host, -1)
	return prefix
}

type Reporter interface {
	Report(*conf, string, RedisStats)
}

type GraphiteReporter struct{}

func (g *GraphiteReporter) Report(cfg *conf, host string, stats RedisStats) {
	prefix := cfg.GetPrefix(host)
	if len(prefix) > 0 && !strings.HasSuffix(prefix, ".") {
		prefix = prefix + "."
	}
	reportTime := time.Now()
	if conn, err := net.Dial("tcp", cfg.GraphiteHost); err == nil {
		defer conn.Close()
		if conn, ok := conn.(*net.TCPConn); ok {
			conn.SetNoDelay(false)
		}
		report := func(name string, value string) {
			if _, err := conn.Write([]byte(name + " " + value + " " + strconv.Itoa(int(reportTime.Unix())) + "\n")); err != nil {
				log.Println("Failed to send stats to graphite @ %s. Error: %s", cfg.GraphiteHost, err.Error())
			}
		}
		for section, values := range stats {
			sectionPrefix := strings.ToLower(section) + "."
			switch section {
			case "Clients", "Memory", "Persistence", "Stats":
				for name, metricValue := range values {
					metricName := prefix + sectionPrefix + name
					report(metricName, metricValue)
				}
			case "Keyspace":
				for db, value := range values {
					dbPrefix := sectionPrefix + db + "."
					vars := strings.Split(value, ",")
					for _, v := range vars {
						if val := strings.Split(v, "="); len(val) == 2 {
							metricName := prefix + dbPrefix + val[0]
							report(metricName, val[1])
						}
					}
				}
			}
		}
	} else {
		log.Printf("Failed to connect to graphite @ %s. Error: %s", cfg.GraphiteHost, err.Error())
	}
}

type RedisStats map[string]map[string]string

func QueryStats(host string) RedisStats {
	if c, err := redis.Dial("tcp", host); err == nil {
		defer c.Close()
		if stats, err := redis.String(c.Do("INFO")); err == nil {
			lines := strings.Split(stats, "\r\n")
			rs := RedisStats(make(map[string]map[string]string))
			var wg map[string]string
			for _, l := range lines {
				if len(l) == 0 {
					wg = nil
					continue
				} else if strings.HasPrefix(l, "#") {
					k := strings.TrimSpace(strings.TrimPrefix(l, "#"))
					wg = make(map[string]string)
					rs[k] = wg
				} else if wg != nil {
					if values := strings.Split(l, ":"); len(values) == 2 {
						if _, err := strconv.ParseFloat(values[1], 64); err == nil {
							switch values[0] {
							case "redis_git_sha1", "redis_git_dirty", "redis_build_id", "arch_bits",
								"process_id", "tcp_port", "aof_enabled":
								// don't report these metrics
								continue
							default:
								wg[values[0]] = values[1]
							}
						}
					}
				}
			}
			return rs
		} else {
			log.Printf("Failed to query stats from redis @ %s. Error: %s", host, err.Error())
			return nil
		}
	} else {
		log.Printf("Failed to connect to redis @ %s. Error: %s", host, err.Error())
		return nil
	}
}

func ctrlc(stop chan<- bool) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)
	go func() {
		forceExit := false
		for _ = range c {
			if forceExit {
				os.Exit(2)
			} else {
				go func() {
					close(stop)
				}()
				forceExit = true
			}
		}
	}()
}

func run(cfg *conf) {
	redisHosts := strings.Split(cfg.RedisHost, ",")
	var reporter Reporter
	reporter = &GraphiteReporter{}
	for _, rhost := range redisHosts {
		if stats := QueryStats(rhost); stats != nil {
			if strings.HasSuffix(rhost, ":6379") {
				rhost = strings.TrimSuffix(rhost, ":6379")
			}
			reporter.Report(cfg, rhost, stats)
		}
	}
}

func daemon(cfg *conf, stop <-chan bool) {
	run(cfg)
	ticker := time.NewTicker(cfg.interval)
	for {
		select {
		case <-ticker.C:
			run(cfg)
		case <-stop:
			return
		}
	}
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	fc := new(conf)
	fc.setFlags()
	flag.Parse()
	if fc.Daemon {
		if d, err := time.ParseDuration(fc.Interval); err == nil {
			fc.interval = d
		} else {
			fmt.Printf("ERROR: Failed to parse duration %s (%s)\n", fc.Interval, err.Error())
			os.Exit(2)
		}
	}

	fmt.Print("Redis Monitor --==--> Graphite\n")
	fmt.Print("\n")
	fmt.Println("[-] Graphite Host:\t" + fc.GraphiteHost + "\n" +
		"[-] Redis Host:\t" + fc.RedisHost + "\n" +
		"[-] Interval:\t" + fc.Interval + "\n" +
		"[-] Prefix:\t" + fc.Prefix)

	stop := make(chan bool)
	ctrlc(stop)

	if fc.Daemon {
		fmt.Println("[-] Running...")
		daemon(fc, stop)
		fmt.Println("[-] Stopped")
	} else {
		run(fc)
	}
}
