package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/cenkalti/backoff"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/kube"
	"github.com/szuecs/kube-static-egress-controller/provider"
	"github.com/szuecs/kube-static-egress-controller/provider/aws"
	"github.com/szuecs/kube-static-egress-controller/provider/noop"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	"k8s.io/client-go/kubernetes"
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

	flushToProviderInterval time.Duration
)

type Config struct {
	Master     string
	KubeConfig string
	DryRun     bool
	LogFormat  string
	LogLevel   string
	Provider   string
	VPCID      string
	// required by AWS provider
	NatCidrBlocks []string
	// required by AWS provider
	AvailabilityZones          []string
	StackTerminationProtection bool
}

var defaultConfig = &Config{
	Master:                     "",
	KubeConfig:                 "",
	VPCID:                      "",
	DryRun:                     false,
	LogFormat:                  "text",
	LogLevel:                   log.InfoLevel.String(),
	Provider:                   "noop",
	StackTerminationProtection: false,
}

func NewConfig() *Config {
	return &Config{}
}

func newProvider(dry bool, name, vpcID string, natCidrBlocks, availabilityZones []string, StackTerminationProtection bool) provider.Provider {
	switch name {
	case aws.ProviderName:
		return aws.NewAwsProvider(dry, vpcID, natCidrBlocks, availabilityZones, StackTerminationProtection)
	case noop.ProviderName:
		return noop.NewNoopProvider()
	default:
		log.Fatalf("Unkown provider: %s", name)
	}
	return nil
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
	app.Flag("vpc-id", "VPC ID (default: auto-detect)").Default(defaultConfig.VPCID).StringVar(&cfg.VPCID)
	app.Flag("aws-nat-cidr-block", "AWS Provider requires to specify NAT-CIDR-Blocks for each AZ to have a NAT gateway in. Each should be a small network having only the NAT GW").StringsVar(&cfg.NatCidrBlocks)
	app.Flag("aws-az", "AWS Provider requires to specify all AZs to have a NAT gateway in.").StringsVar(&cfg.AvailabilityZones)
	app.Flag("stack-termination-protection", "Enables AWS clouformation stack termination protection for the stacks managed by the controller.").BoolVar(&cfg.StackTerminationProtection)
	app.Flag("flush-interval", "Minimum interval to call provider on change events.").Default("5s").DurationVar(&flushToProviderInterval)
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

	p := newProvider(cfg.DryRun, cfg.Provider, cfg.VPCID, cfg.NatCidrBlocks, cfg.AvailabilityZones, cfg.StackTerminationProtection)
	run(newKubeClient(), p)
}

// newKubeClient returns a new Kubernetes client with the default config.
func newKubeClient() kubernetes.Interface {
	var kubeconfig string
	if _, err := os.Stat(clientcmd.RecommendedHomeFile); err == nil {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	log.Debugf("use config file %s", kubeconfig)
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("build config failed: %v", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("initialize kubernetes client failed: %v", err)
	}
	log.Infof("Connected to cluster at %s", config.Host)

	return client
}

func run(client kubernetes.Interface, p provider.Provider) {
	quitCH := make(chan struct{})
	var wg sync.WaitGroup

	watcher, err := kube.NewConfigMapWatcher(client, "", "egress=static", quitCH)
	if err != nil {
		log.Fatalf("Failed to create ConfigMapWatcher: %v", err)
	}

	watchCH := make(chan map[string][]string, 2)

	// init sync
	wg.Add(1)
	go initSync(watcher, &wg, watchCH)

	// watcher
	wg.Add(1)
	go enterWatcher(watcher, &wg, watchCH)

	// merger
	providerCH := make(chan []string)
	wg.Add(1)
	go enterMerger(&wg, watchCH, providerCH, quitCH)

	// Provider
	wg.Add(1)
	go enterProvider(&wg, p, providerCH, quitCH)

	// quit
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	for i := 0; i < 3; i++ {
		log.Infof("send quit to all %d", i)
		quitCH <- struct{}{}
	}
	wg.Wait()
	log.Infoln("shutdown")
}

func initSync(watcher *kube.ConfigMapWatcher, wg *sync.WaitGroup, mergerCH chan<- map[string][]string) {
	defer wg.Done()
	log.Debugln("Init: get initial configmap data")
	defer log.Infoln("Init: quit")
	list, err := watcher.ListConfigMaps()
	if err != nil {
		log.Fatalf("Init: Failed to list ConfigMaps: %v", err)
	}
	log.Infof("Init: Bootstrap list: %v", list)
	mergerCH <- list
}

func enterWatcher(watcher *kube.ConfigMapWatcher, wg *sync.WaitGroup, mergerCH chan<- map[string][]string) {
	defer wg.Done()
	defer log.Infoln("Watcher: quit")
	err := watcher.WatchConfigMaps(mergerCH)
	if err != nil {
		log.Fatalf("Watcher: Failed to enter ConfigMap watcher: %v", err)
	}
}

func enterMerger(wg *sync.WaitGroup, watcherCH <-chan map[string][]string, providerCH chan<- []string, quitCH <-chan struct{}) {
	defer wg.Done()
	log.Debugln("Merger: start")
	result := make(map[string][]string, 0)
	for {
		select {
		case m := <-watcherCH:
			for k, v := range m {
				result[k] = v
				log.Debugf("Merger: %s -> %v", k, v)
			}

		case <-time.After(flushToProviderInterval):
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
}

func enterProvider(wg *sync.WaitGroup, p provider.Provider, mergerCH <-chan []string, quitCH <-chan struct{}) {
	retry := backoff.NewConstantBackOff(5 * time.Minute)
	defer wg.Done()
	bootstrap := true
	resultCache := make([]string, 0)
	for {
		select {
		case input := <-mergerCH:
			output := make([]string, 0)
			for _, s := range input {
				_, ipnet, err := net.ParseCIDR(s)
				if err != nil {
					log.Warningf("Provider(%s): skipping not parseable CIDR: %s, err: %v", p, s, err)
					continue
				}
				output = append(output, ipnet.String())
			}
			sort.SliceStable(output, func(i, j int) bool {
				return output[i] < output[j]
			})

			if bootstrap {
				log.Infof("Provider(%s): bootstrapped", p)
				bootstrap = false
				continue
			}
			var err error
			createNotify := func(err error, t time.Duration) {
				switch errors.Cause(err).(type) {
				case *provider.AlreadyExistsError:
					err = p.Update(output)
					if err != nil {
						log.Error(err)
					}
				}
			}
			if len(input) == 0 { // not caused by faulty value in CIDR string
				if !sameValues(resultCache, output) {
					resultCache = output
					log.Infof("Provider(%s): no targets -> delete", p)
					err = p.Delete()
				} else {
					log.Debugf("Provider(%s): Delete change was already done", p)
				}
			} else if len(output) > 0 {
				if !sameValues(resultCache, output) {
					log.Infof("Provider(%s): got %d targets, cached: %d", p, len(output), len(resultCache))
					if len(resultCache) == 0 {
						createFunc := func() error {
							return p.Create(output)
						}
						err = backoff.RetryNotify(createFunc, retry, createNotify)
						if err != nil {
							log.Error(err)
						}
					} else {
						err = p.Update(output)
						// create if stack does not exist, but we have targets
						if err != nil {
							switch errors.Cause(err).(type) {
							case *provider.DoesNotExistError:
								createFunc := func() error {
									return p.Create(output)
								}
								err = backoff.RetryNotify(createFunc, retry, createNotify)
								if err != nil {
									log.Error(err)
								}
							default:
								log.Errorf("Failed to update stack: %v", err)
							}
						}
					}
					resultCache = output
				}
			} else {
				log.Infof("Provider(%s): got no targets could be a failure -> do nothing", p)
			}
			if err != nil {
				log.Errorf("Provider(%s): Failed to execute with %d targets (%v): %v", p, len(output), output, err)
			}

		case <-quitCH:
			log.Infof("Provider(%s): got quit signal", p)
			return
		}
	}

}

func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
