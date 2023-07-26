package config

import "github.com/BurntSushi/toml"

type Config struct {
	FFMPEGPath             string   `toml:"ffmpeg_path"`
	Device                 string   `toml:"device"`
	SaveTSDir              string   `toml:"save_ts_dir"`
	Bucket                 string   `toml:"bucket"`
	WebhookURL             string   `toml:"webhook_url"`
	LoadKernelModule       bool     `toml:"load_kernel_module"`
	AWSCreds               *AWSCred `toml:"aws_creds"`
	WebserverListenAddr    string   `toml:"webserver_listen_address"`
	DisableRecordingForIPs []string `toml:"disable_recording_for_ips"`
}

type AWSCred struct {
	AccessKeyID     string `toml:"access_key_id"`
	SecretAccessKey string `toml:"secret_access_key"`
}

func LoadConfig(confPath string) (*Config, error) {
	var c Config
	_, err := toml.DecodeFile(confPath, &c)
	if err != nil {
		return nil, err
	}

	if c.FFMPEGPath == "" {
		c.FFMPEGPath = "ffmpeg"
	}

	if c.Device == "" {
		c.Device = "/dev/video0"
	}

	return &c, nil
}
