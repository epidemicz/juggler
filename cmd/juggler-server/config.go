package main

import (
	"errors"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v2"
)

// Redis defines the redis-specific configuration options.
type Redis struct {
	Addr        string        `yaml:"addr"`
	MaxActive   int           `yaml:"max_active"`
	MaxIdle     int           `yaml:"max_idle"`
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	PubSub      *Redis        `yaml:"pubsub"`
	Caller      *Redis        `yaml:"caller"`
}

// CallerBroker defines the configuration options for the caller broker.
type CallerBroker struct {
	BlockingTimeout time.Duration `yaml:"blocking_timeout"`
	CallCap         int           `yaml:"call_cap"`
}

// Server defines the juggler server configuration options.
type Server struct {
	// HTTP server configuration for the websocket handshake/upgrade
	Addr               string        `yaml:"addr"`
	Paths              []string      `yaml:"paths"`
	MaxHeaderBytes     int           `yaml:"max_header_bytes"`
	ReadBufferSize     int           `yaml:"read_buffer_size"`
	WriteBufferSize    int           `yaml:"write_buffer_size"`
	HandshakeTimeout   time.Duration `yaml:"handshake_timeout"`
	WhitelistedOrigins []string      `yaml:"whitelisted_origins"`

	// websocket/juggler configuration
	ReadLimit               int64         `yaml:"read_limit"`
	ReadTimeout             time.Duration `yaml:"read_timeout"`
	WriteLimit              int64         `yaml:"write_limit"`
	WriteTimeout            time.Duration `yaml:"write_timeout"`
	AcquireWriteLockTimeout time.Duration `yaml:"acquire_write_lock_timeout"`
	AllowEmptySubprotocol   bool          `yaml:"allow_empty_subprotocol"`

	// handler options
	CloseURI string `yaml:"close_uri"`
	PanicURI string `yaml:"panic_uri"`
}

// Config defines the configuration options of the server.
type Config struct {
	Redis        *Redis        `yaml:"redis"`
	CallerBroker *CallerBroker `yaml:"caller_broker"`
	Server       *Server       `yaml:"server"`
}

func getDefaultConfig() *Config {
	return &Config{
		Redis: &Redis{
			Addr:        *redisAddrFlag,
			MaxActive:   0,
			MaxIdle:     0,
			IdleTimeout: 0,
		},
		CallerBroker: &CallerBroker{
			BlockingTimeout: 0,
			CallCap:         0,
		},
		Server: &Server{
			Addr:                    ":" + strconv.Itoa(*portFlag),
			Paths:                   []string{"/ws"},
			ReadLimit:               0,
			ReadTimeout:             0,
			WriteLimit:              0,
			WriteTimeout:            0,
			AcquireWriteLockTimeout: 0,
			AllowEmptySubprotocol:   *allowEmptyProtoFlag,
			CloseURI:                "",
		},
	}
}

func getConfigFromReader(r io.Reader) (*Config, error) {
	conf := getDefaultConfig()

	// set default values
	if r != nil {
		b, err := ioutil.ReadAll(r)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, conf); err != nil {
			return nil, err
		}
	}
	return conf, nil
}

func getConfigFromFile(file string) (*Config, error) {
	var r io.Reader
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		r = f
	}
	return getConfigFromReader(r)
}

var zeroRedis = Redis{}

func isZeroRedis(rc *Redis) bool {
	if rc == nil {
		return true
	}

	// nil the pubsub and caller
	copy := *rc
	copy.PubSub = nil
	copy.Caller = nil
	return copy == zeroRedis
}

// check redis configuration: use Config.Redis to use the same pool
// for pubsub and caller, or use Config.Redis.PubSub and Config.Redis.Caller.
// No other combination is accepted.
func checkRedisConfig(conf *Redis) error {
	// if either PubSub or Caller is set, then both must be set
	if !isZeroRedis(conf.PubSub) || !isZeroRedis(conf.Caller) {
		if (conf.PubSub == nil || conf.PubSub.Addr == "") || (conf.Caller == nil || conf.Caller.Addr == "") {
			return errors.New("both redis.pubsub and redis.caller sections must be configured")
		}

		// and the generic redis must not be set
		if conf.Addr == *redisAddrFlag {
			conf.Addr = ""
		}
		if !isZeroRedis(conf) {
			return errors.New("redis must not be configured if redis.pubsub and redis.caller are configured")
		}
	}
	return nil
}
