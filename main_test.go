package main

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/szuecs/kube-static-egress-controller/kube"
	provider "github.com/szuecs/kube-static-egress-controller/provider"
	"github.com/szuecs/kube-static-egress-controller/provider/noop"
)

func TestNewConfig(t *testing.T) {
	tests := []struct {
		name string
		want *Config
	}{
		{
			name: "test-new-config",
			want: &Config{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewConfig(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_ParseFlags(t *testing.T) {
	type fields struct {
		Master            string
		KubeConfig        string
		DryRun            bool
		LogFormat         string
		LogLevel          string
		Provider          string
		NatCidrBlocks     []string
		AvailabilityZones []string
	}
	type args struct {
		args []string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name:    "test-no-args-config",
			fields:  fields{}, // TODO
			args:    args{},   // TODO
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO
			// cfg := &Config{
			// 	Master:            tt.fields.Master,
			// 	KubeConfig:        tt.fields.KubeConfig,
			// 	DryRun:            tt.fields.DryRun,
			// 	LogFormat:         tt.fields.LogFormat,
			// 	LogLevel:          tt.fields.LogLevel,
			// 	Provider:          tt.fields.Provider,
			// 	NatCidrBlocks:     tt.fields.NatCidrBlocks,
			// 	AvailabilityZones: tt.fields.AvailabilityZones,
			// }
			// if err := cfg.ParseFlags(tt.args.args); (err != nil) != tt.wantErr {
			// 	t.Errorf("Config.ParseFlags() error = %v, wantErr %v", err, tt.wantErr)
			// }
		})
	}
}

func Test_initSync(t *testing.T) {
	tests := []struct {
		name     string
		watcher  *kube.ConfigMapWatcher
		wg       sync.WaitGroup
		mergerCH chan map[string][]string
		want     map[string][]string
	}{
		{
			name:     "initSync ..",
			watcher:  nil, // TODO
			wg:       sync.WaitGroup{},
			mergerCH: make(chan map[string][]string, 2),
			want:     map[string][]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO
		})
	}
}

func Test_enterWatcher(t *testing.T) {
	tests := []struct {
		name     string
		watcher  *kube.ConfigMapWatcher
		wg       sync.WaitGroup
		mergerCH chan map[string][]string
		want     map[string][]string
	}{
		{
			name:     "enterWatcher ..",
			watcher:  nil, // TODO
			wg:       sync.WaitGroup{},
			mergerCH: make(chan map[string][]string, 2),
			want:     map[string][]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO
		})
	}
}

func Test_enterMerger(t *testing.T) {
	tests := []struct {
		name       string
		wg         sync.WaitGroup
		mergerCH   chan map[string][]string
		providerCH chan []string
		quitCH     chan struct{}
		want       map[string][]string
	}{
		{
			name:       "enterMerger ..",
			wg:         sync.WaitGroup{},
			mergerCH:   make(chan map[string][]string, 2),
			providerCH: make(chan []string),
			quitCH:     make(chan struct{}),
			want:       map[string][]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO

			// select {
			//   case <-thingHappened:
			//   case <-time.After(timeout):
			// 	  t.Fatal("timeout")
			//   }
		})
	}
}

func Test_enterProvider(t *testing.T) {
	tests := []struct {
		name     string
		wg       sync.WaitGroup
		p        provider.Provider
		mergerCH chan []string
		quitCH   chan struct{}
		timeout  time.Duration
		action   func(context.Context) (bool, error)
		want     bool
	}{
		{
			name:     "enterProvider noop quit test",
			wg:       sync.WaitGroup{},
			p:        provider.NewProvider(true, noop.ProviderName, []string{}, []string{}, false),
			mergerCH: make(chan []string),
			quitCH:   make(chan struct{}),
			timeout:  3 * time.Second,
			action:   func(ctx context.Context) (bool, error) { return true, nil },
			want:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.wg.Add(1)
			go enterProvider(&tt.wg, tt.p, tt.mergerCH, tt.quitCH)

			ctx := context.Background()
			ctx, cancel := context.WithTimeout(ctx, tt.timeout)
			defer cancel()
			got, err := tt.action(ctx)
			if err != nil {
				t.Errorf("Failed to run action for %s: %v", tt.name, err)
			}
			if got != tt.want {
				t.Errorf("Failed by result: %v != %v", got, tt.want)
			}
			if err = ctx.Err(); err != nil {
				t.Errorf("Failed context was canceled: %v", err)
			}

			tt.quitCH <- struct{}{}
			tt.wg.Wait()
		})
	}
}

func Test_sameValues(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{
			name: "sameValues empty slices",
			a:    []string{},
			b:    []string{},
			want: true,
		},
		{
			name: "sameValues one empty slice a",
			a:    []string{},
			b:    []string{"foo"},
			want: false,
		},
		{
			name: "sameValues one empty slice b",
			a:    []string{"foo"},
			b:    []string{},
			want: false,
		},
		{
			name: "sameValues non empty slices one value same",
			a:    []string{"foo"},
			b:    []string{"foo"},
			want: true,
		},
		{
			name: "sameValues non empty slices one value different",
			a:    []string{"foo"},
			b:    []string{"bar"},
			want: false,
		},
		{
			name: "sameValues non empty slices multi values same",
			a:    []string{"baz", "foo", "bar"},
			b:    []string{"baz", "foo", "bar"},
			want: true,
		},
		{
			name: "sameValues non empty slices multi values different",
			a:    []string{"baz", "foo", "bar"},
			b:    []string{"foo", "bar", "baz"},
			want: false,
		},
		{
			name: "sameValues different number of values a",
			a:    []string{"foo", "bar", "baz", "new"},
			b:    []string{"foo", "bar", "baz"},
			want: false,
		},
		{
			name: "sameValues different number of values b",
			a:    []string{"foo", "bar", "baz"},
			b:    []string{"foo", "bar", "baz", "new"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameValues(tt.a, tt.b); got != tt.want {
				t.Errorf("failed %v != %v, a: %v, b: %v", got, tt.want, tt.a, tt.b)
			}
		})
	}
}
