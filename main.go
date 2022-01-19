package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/controller"
	"github.com/szuecs/kube-static-egress-controller/kube"
	"github.com/szuecs/kube-static-egress-controller/provider"
	"github.com/szuecs/kube-static-egress-controller/provider/aws"
	"github.com/szuecs/kube-static-egress-controller/provider/noop"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	v1 "k8s.io/api/core/v1"
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
)

type Config struct {
	Master             string
	KubeConfig         string
	DryRun             bool
	LogFormat          string
	LogLevel           string
	Provider           string
	VPCID              string
	ClusterID          string
	ClusterIDTagPrefix string
	ControllerID       string
	// required by AWS provider
	NatCidrBlocks []string
	// required by AWS provider
	AvailabilityZones          []string
	StackTerminationProtection bool
	AdditionalStackTags        map[string]string
	Namespace                  string
	ResyncInterval             time.Duration
	Address                    string
}

var defaultConfig = &Config{
	Master:                     "",
	KubeConfig:                 "",
	VPCID:                      "",
	ClusterID:                  "",
	ClusterIDTagPrefix:         "kubernetes.io/cluster/",
	ControllerID:               "kube-static-egress-controller",
	DryRun:                     false,
	LogFormat:                  "text",
	LogLevel:                   log.InfoLevel.String(),
	Provider:                   "noop",
	StackTerminationProtection: false,
	Namespace:                  v1.NamespaceAll,
	Address:                    ":8080",
}

func NewConfig() *Config {
	return &Config{
		AdditionalStackTags: make(map[string]string),
	}
}

func newProvider(clusterID, controllerID string, dry bool, name, vpcID string, clusterIDTagPrefix string, natCidrBlocks, availabilityZones []string, stackTerminationProtection bool, additionalStackTags map[string]string) provider.Provider {
	switch name {
	case aws.ProviderName:
		return aws.NewAWSProvider(clusterID, controllerID, dry, vpcID, clusterIDTagPrefix, natCidrBlocks, availabilityZones, stackTerminationProtection, additionalStackTags)
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
	app.Flag("cluster-id", "Cluster ID used define ownership of Egress stack.").StringVar(&cfg.ClusterID)
	app.Flag("cluster-id-tag-prefix", "Prefix for the Cluster ID tag set on the Egress stack.").Default(defaultConfig.ClusterIDTagPrefix).StringVar(&cfg.ClusterIDTagPrefix)
	app.Flag("controller-id", "Controller ID used to identify ownership of Egress stack.").Default(defaultConfig.ControllerID).StringVar(&cfg.ControllerID)
	app.Flag("vpc-id", "VPC ID (default: auto-detect)").Default(defaultConfig.VPCID).StringVar(&cfg.VPCID)
	app.Flag("aws-nat-cidr-block", "AWS Provider requires to specify NAT-CIDR-Blocks for each AZ to have a NAT gateway in. Each should be a small network having only the NAT GW").StringsVar(&cfg.NatCidrBlocks)
	app.Flag("aws-az", "AWS Provider requires to specify all AZs to have a NAT gateway in.").StringsVar(&cfg.AvailabilityZones)
	app.Flag("stack-termination-protection", "Enables AWS clouformation stack termination protection for the stacks managed by the controller.").BoolVar(&cfg.StackTerminationProtection)
	app.Flag("additional-stack-tags", "Set additional custom tags on the Cloudformation Stacks managed by the controller.").StringMapVar(&cfg.AdditionalStackTags)
	app.Flag("resync-interval", "Resync interval to make sure current state is actual state.").Default("5m").DurationVar(&cfg.ResyncInterval)
	app.Flag("dry-run", "When enabled, prints changes rather than actually performing them (default: disabled)").BoolVar(&cfg.DryRun)
	app.Flag("log-level", "Set the level of logging. (default: info, options: panic, debug, info, warn, error, fatal").Default(defaultConfig.LogLevel).EnumVar(&cfg.LogLevel, allLogLevelsAsStrings()...)
	app.Flag("namespace", "Limit controller to single namespace. (default: all namespaces").Default(defaultConfig.Namespace).StringVar(&cfg.Namespace)
	app.Flag("address", "The address to listen on. (default: ':8080'").Default(defaultConfig.Address).StringVar(&cfg.Address)
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

	p := newProvider(cfg.ClusterID, cfg.ControllerID, cfg.DryRun, cfg.Provider, cfg.VPCID, cfg.ClusterIDTagPrefix, cfg.NatCidrBlocks, cfg.AvailabilityZones, cfg.StackTerminationProtection, cfg.AdditionalStackTags)

	configsChan := make(chan provider.EgressConfig)
	cmWatcher, err := kube.NewConfigMapWatcher(newKubeClient(), cfg.Namespace, "egress=static", configsChan)
	if err != nil {
		log.Fatalf("Failed to setup ConfigMap watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go handleSigterm(cancel)

	go cmWatcher.Run(ctx)

	handler := http.NewServeMux()
	handler.Handle("/metrics", promhttp.Handler())
	go serve(ctx, cfg.Address, handler)

	controller := controller.NewEgressController(p, cmWatcher, cfg.ResyncInterval)
	controller.Run(ctx)
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

// handleSigterm handles SIGTERM signal sent to the process.
func handleSigterm(cancelFunc func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	<-signals
	log.Info("Received Term signal. Terminating...")
	cancelFunc()
}

func serve(ctx context.Context, address string, handler http.Handler) {
	server := http.Server{
		Addr:    address,
		Handler: handler,
	}

	log.Infof("Starting server on %s", address)

	go func() {
		<-ctx.Done()
		log.Infof("Shutting down server ...")
		server.Shutdown(ctx)
	}()

	err := server.ListenAndServe()
	if err != nil {
		if err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}
