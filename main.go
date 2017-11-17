package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/kube"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kops/protokube/pkg/gossip/dns/provider"
)

var (
	// set at link time
	version = "unknown"
)

type Config struct {
	Master     string
	KubeConfig string
	DryRun     bool
	LogFormat  string
	LogLevel   string
	Provider   string
}

var defaultConfig = &Config{
	Master:     "",
	KubeConfig: "",
	DryRun:     false,
	LogFormat:  "text",
	LogLevel:   log.InfoLevel.String(),
	Provider:   "noop",
}

func NewConfig() *Config {
	return &Config{}
}

func allLogLevelsAsStrings() []string {
	var levels []string
	for _, level := range log.AllLevels {
		levels = append(levels, level.String())
	}
	return levels
}
func (cfg *Config) ParseFlags(args []string) error {
	app := kingpin.New("kube-static-egress-controller", "TODO")
	app.Version(version)
	app.DefaultEnvars()

	// Flags related to Kubernetes
	app.Flag("master", "The Kubernetes API server to connect to (default: auto-detect)").Default(defaultConfig.Master).StringVar(&cfg.Master)
	app.Flag("kubeconfig", "Retrieve target cluster configuration from a Kubernetes configuration file (default: auto-detect)").Default(defaultConfig.KubeConfig).StringVar(&cfg.KubeConfig)
	app.Flag("provider", "Provider implementing static egress <noop|aws> (default: auto-detect)").Default(defaultConfig.Provider).StringVar(&cfg.Provider)
	app.Flag("dry-run", "When enabled, prints changes rather than actually performing them (default: disabled)").BoolVar(&cfg.DryRun)
	app.Flag("log-level", "Set the level of logging. (default: info, options: panic, debug, info, warn, error, fatal").Default(defaultConfig.LogLevel).EnumVar(&cfg.LogLevel, allLogLevelsAsStrings()...)
	_, err := app.Parse(args)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	cfg := NewConfig()
	err := cfg.ParseFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("flag parsing error: %v", err)
	}
	if cfg.LogFormat == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}
	if cfg.DryRun {
		log.Info("running in dry-run mode. No changes to DNS records will be made.")
	}

	ll, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		log.Fatalf("failed to parse log level: %v", err)
	}
	log.SetLevel(ll)

	log.Printf("CFG: %+v", cfg) //TODO(sszuecs): drop it

	config, err := clientcmd.BuildConfigFromFlags(cfg.Master, cfg.KubeConfig)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	var natCidrBlocks, availabilityZones []string // TODO(sszuecs): flags
	p := provider.New(cfg.Provider, natCidrBlocks, availabilityZones)
	run(config, cfg.DryRun)
}

func run(config *restclient.Config, dry bool) {
	quitCH := make(chan struct{})
	var wg sync.WaitGroup

	watcher, err := kube.NewConfigMapWatcher(config, "", "egress=static", quitCH)
	if err != nil {
		log.Fatalf("Failed to create ConfigMapWatcher: %v", err)
	}

	watchCH := make(chan map[string][]string, 2)

	// init sync
	wg.Add(1)
	go func(ch chan<- map[string][]string) {
		defer wg.Done()
		log.Debugln("Init: get initial configmap data")
		defer log.Infoln("Init: quit")
		list, err := watcher.ListConfigMaps()
		if err != nil {
			log.Fatalf("Init: Failed to list ConfigMaps: %v", err)
		}
		log.Infof("Init: Bootstrap list: %v", list)
		ch <- list
	}(watchCH)

	// watcher
	wg.Add(1)
	go func(ch chan<- map[string][]string) {
		defer wg.Done()
		defer log.Infoln("Watcher: quit")
		log.Debugln("Watcher: start")
		err = watcher.WatchConfigMaps(ch)
		if err != nil {
			log.Fatalf("Watcher: Failed to enter ConfigMap watcher: %v", err)
		}
	}(watchCH)

	// merger
	cfCH := make(chan []string)
	wg.Add(1)
	go func(ch <-chan map[string][]string, cfCH chan<- []string, quitCH <-chan struct{}) {
		defer wg.Done()
		log.Debugln("Merger: start")
		result := make(map[string][]string, 0)
		for {
			select {
			case m := <-ch:
				for k, v := range m {
					result[k] = v
					log.Debugf("Merger: %s -> %v", k, v)
				}

			case <-time.After(5 * time.Second):
				log.Infof("Merger: current state: %v", result)
				res := make([]string, 0)
				uniquer := map[string]bool{}
				for _, v := range result {
					for _, s := range v {
						if _, ok := uniquer[s]; !ok {
							uniquer[s] = true
							res = append(res, s)
						}
					}
				}
				cfCH <- res
			case <-quitCH:
				log.Infoln("Merger: got quit signal")
				return
			}
		}
	}(watchCH, cfCH, quitCH)

	// CF
	wg.Add(1)
	go func(cfCH <-chan []string, quitCH <-chan struct{}) {
		defer wg.Done()
		for {
			select {
			case a := <-cfCH:
				// TODO(sszuecs): parse and valid check CIDR
				log.Infof("CF: got: %v", a)
				if !dry {
					log.Debugln("CF: execute")
				}
			case <-quitCH:
				log.Infoln("CF: got quit signal")
				return
			}
		}
	}(cfCH, quitCH)

	// quit
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	// Block until a signal is received.
	<-sigs
	for i := 0; i < 3; i++ {
		log.Infof("send quit to all %d", i)
		quitCH <- struct{}{}
	}
	wg.Wait()
	log.Infoln("shutdown")
}
