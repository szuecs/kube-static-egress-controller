package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/kube"
	provider "github.com/szuecs/kube-static-egress-controller/provider"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	name = "kube-static-egress-controller"
)

var (
	// set at link time
	version    = "unknown"
	buildstamp = "unknown"
	githash    = "unknown"
)

type Config struct {
	Master     string
	KubeConfig string
	DryRun     bool
	LogFormat  string
	LogLevel   string
	Provider   string
	// required by AWS provider
	NatCidrBlocks []string
	// required by AWS provider
	AvailabilityZones []string
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
	app := kingpin.New(name, fmt.Sprintf(`%s
watches for Kubernetes Configmaps that
are having the label egress=static.  It will read all data entries and
try to use values as CIDR network targets.  These target networks will
be routed through a static pool of IPs, such that the destination can
be sure, your requests to these destination will be served from a
small list of IPs.

Example:

    %s --provider=aws --aws-nat-cidr-block="172.31.64.0/28" --aws-nat-cidr-block=172.31.64.16/28 --aws-nat-cidr-block=172.31.64.32/28 --aws-az=eu-central-1a --aws-az=eu-central-1b --aws-az=eu-central-1c --dry-run
`, name, name))
	app.Version(version + "\nbuild time: " + buildstamp + "\nGit ref: " + githash)
	app.DefaultEnvars()

	// Flags related to Kubernetes
	app.Flag("master", "The Kubernetes API server to connect to (default: auto-detect)").Default(defaultConfig.Master).StringVar(&cfg.Master)
	app.Flag("kubeconfig", "Retrieve target cluster configuration from a Kubernetes configuration file (default: auto-detect)").Default(defaultConfig.KubeConfig).StringVar(&cfg.KubeConfig)
	app.Flag("provider", "Provider implementing static egress <noop|aws> (default: auto-detect)").Default(defaultConfig.Provider).StringVar(&cfg.Provider)
	app.Flag("aws-nat-cidr-block", "AWS Provider requires to specify NAT-CIDR-Blocks for each AZ to have a NAT gateway in. Each should be a small network having only the NAT GW").StringsVar(&cfg.NatCidrBlocks)
	app.Flag("aws-az", "AWS Provider requires to specify all AZs to have a NAT gateway in.").StringsVar(&cfg.AvailabilityZones)
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
		log.Fatalf("Flag parsing error: %v", err)
	}
	if cfg.LogFormat == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}
	if cfg.DryRun {
		log.Info("Running in dry-run mode. No changes will be made.")
	}

	ll, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		log.Fatalf("Failed to parse log level: %v", err)
	}
	log.SetLevel(ll)
	log.Debugf("config: %+v", cfg)

	config, err := clientcmd.BuildConfigFromFlags(cfg.Master, cfg.KubeConfig)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	p := provider.NewProvider(cfg.DryRun, cfg.Provider, cfg.NatCidrBlocks, cfg.AvailabilityZones)
	run(config, p)
}

func run(config *restclient.Config, p provider.Provider) {
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
	providerCH := make(chan []string)
	wg.Add(1)
	go func(ch <-chan map[string][]string, providerCH chan<- []string, quitCH <-chan struct{}) {
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
				log.Debugf("Merger: current state: %v", result)
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
				providerCH <- res
			case <-quitCH:
				log.Infoln("Merger: got quit signal")
				return
			}
		}
	}(watchCH, providerCH, quitCH)

	// Provider
	wg.Add(1)
	go func(providerCH <-chan []string, quitCH <-chan struct{}) {
		defer wg.Done()
		for {
			select {
			case a := <-providerCH:
				// TODO(sszuecs): parse and valid check CIDR
				log.Infof("Provider: got targets: %v", a)
				err := p.Execute(a)
				if err != nil {
					log.Errorf("Failed to execute provider(%s): %v", p, a)
				}

			case <-quitCH:
				log.Infoln("Provider: got quit signal")
				return
			}
		}
	}(providerCH, quitCH)

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
