package sshark

import (
	"github.com/kylelemons/go-gypsy/yaml"
	"strconv"
	"time"
)

type Config struct {
	MessageBus        MessageBusConfig
	AdvertiseInterval time.Duration
	WardenSocketPath  string
	StateFilePath     string
}

type MessageBusConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

var DefaultConfig = Config{
	MessageBus: MessageBusConfig{
		Host: "127.0.0.1",
		Port: 4222,
	},

	WardenSocketPath: "/tmp/warden.sock",
	StateFilePath:    "/tmp/sshark.json",

	AdvertiseInterval: 10 * time.Second,
}

func LoadConfig(configFilePath string) Config {
	file := yaml.ConfigFile(configFilePath)

	mbusHost := file.Require("message_bus.host")
	mbusPort, err := strconv.Atoi(file.Require("message_bus.port"))
	if err != nil {
		panic("non-numeric message bus port")
	}

	mbusUsername, _ := file.Get("message_bus.username")
	mbusPassword, _ := file.Get("message_bus.password")

	wardenSocketPath := file.Require("warden_socket")
	stateFilePath, _ := file.Get("state_file")

	advertiseInterval, err := strconv.Atoi(file.Require("advertise_interval"))
	if err != nil {
		panic("non-numeric advertise interval")
	}

	return Config{
		MessageBus: MessageBusConfig{
			Host:     mbusHost,
			Port:     mbusPort,
			Username: mbusUsername,
			Password: mbusPassword,
		},

		AdvertiseInterval: time.Duration(advertiseInterval) * time.Second,

		WardenSocketPath: wardenSocketPath,
		StateFilePath:    stateFilePath,
	}
}
