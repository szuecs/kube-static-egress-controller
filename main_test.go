package main

import (
	"reflect"
	"testing"
)

func TestNewConfig(t *testing.T) {
	tests := []struct {
		name string
		want *Config
	}{
		{
			name: "test-new-config",
			want: &Config{
				AdditionalStackTags: make(map[string]string),
			},
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
