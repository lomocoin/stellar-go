package main

import (
	stdLog "log"
	"net/url"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stellar/go/network"
	horizon "github.com/stellar/go/services/horizon/internal"
	"github.com/stellar/go/services/horizon/internal/db2/schema"
	apkg "github.com/stellar/go/support/app"
	"github.com/stellar/go/support/log"
	"github.com/stellar/go/support/strutils"
	"github.com/throttled/throttled"
)

var app *horizon.App
var config horizon.Config
var c horizon.Config

var rootCmd *cobra.Command

var tlsProvided = 0

// flagType implements a generic interface for the different command line flags,
// allowing them to be configured in a uniform way.
type flagType func(name string, value interface{}, usage string) interface{}

var (
	stringFlag flagType = func(name string, value interface{}, usage string) interface{} {
		return rootCmd.PersistentFlags().String(name, value.(string), usage)
	}
	intFlag flagType = func(name string, value interface{}, usage string) interface{} {
		return rootCmd.PersistentFlags().Int(name, value.(int), usage)
	}
	uintFlag flagType = func(name string, value interface{}, usage string) interface{} {
		return rootCmd.PersistentFlags().Uint(name, value.(uint), usage)
	}
	boolFlag flagType = func(name string, value interface{}, usage string) interface{} {
		return rootCmd.PersistentFlags().Bool(name, value.(bool), usage)
	}
)

// configOption is a complete description of the configuration of a command line option
type configOption struct {
	name           string              // e.g. "db-url"
	envVar         string              // e.g. "DATABASE_URL". Defaults to uppercase/underscore representation of name
	flagType       flagType            // e.g. boolFlag
	flagDefault    interface{}         // A default if no option is provided. Set to "" if no default
	required       bool                // Whether this option must be set for Horizon to run
	usage          string              // Help text
	customSetValue func(*configOption) // Optional function for custom validation/transformation
	configKey      interface{}         // Pointer to the final key in the horizon.Config struct
}

// require checks that a required string configuration option is not empty, raising a user error if it is.
func (co *configOption) require() {
	if co.required == true && viper.GetString(co.name) == "" {
		stdLog.Fatalf("Invalid config: %s is blank. Please specify --%s on the command line or set the %s environment variable.", co.name, co.name, co.envVar)
	}
}

// setValue sets a value in the global config, using a custom function, if one was provided.
func (co *configOption) setValue(c *horizon.Config) error {
	// Use a custom setting function, if one is provided
	if co.customSetValue != nil {
		co.customSetValue(co)
		// Otherwise, just set the provided arg directly
	} else if co.configKey != nil {
		co.setSimpleValue()
	}
	return nil
}

// setSimpleValue sets the value of a configOption's configKey, based on the configOption's default type.
func (co *configOption) setSimpleValue() {
	if co.configKey != nil {
		switch co.flagDefault.(type) {
		case string:
			*(co.configKey.(*string)) = viper.GetString(co.name)
		case int:
			*(co.configKey.(*int)) = viper.GetInt(co.name)
		case bool:
			*(co.configKey.(*bool)) = viper.GetBool(co.name)
		case uint:
			*(co.configKey.(*uint)) = uint(viper.GetInt(co.name))
		}
	}
}

// setFlag sets the correct pFlag type, based on the configOption's default type.
func (co *configOption) setFlag() {
	switch co.flagDefault.(type) {
	case string:
		stringFlag(co.name, co.flagDefault, co.usage)
	case int:
		intFlag(co.name, co.flagDefault, co.usage)
	case bool:
		boolFlag(co.name, co.flagDefault, co.usage)
	case uint:
		uintFlag(co.name, co.flagDefault, co.usage)
	}
}

// setDuration converts a command line int to a duration, and stores it in the final config.
func setDuration(co *configOption) {
	*(co.configKey.(*time.Duration)) = time.Duration(viper.GetInt(co.name)) * time.Second
}

// setURL converts a command line string to a URL, and stores it in the final config.
func setURL(co *configOption) {
	urlString := viper.GetString(co.name)
	if urlString != "" {
		urlType, err := url.Parse(urlString)
		if err != nil {
			stdLog.Fatalf("Unable to parse URL: %s/%v", urlString, err)
		}
		*(co.configKey.(*url.URL)) = *urlType
	}
}

// setLogLevel validates and sets the log level globally and in the final config.
func setLogLevel(co *configOption) {
	ll, err := logrus.ParseLevel(viper.GetString(co.name))
	if err != nil {
		stdLog.Fatalf("Could not parse log-level: %v", viper.GetString(co.name))
	}
	log.DefaultLogger.Level = ll
	*(co.configKey.(*logrus.Level)) = ll
}

// setLogFile configures a log file for writing, ands sets the file name in the final config.
func setLogFile(co *configOption) {
	lf := viper.GetString("log-file")
	if lf != "" {
		logFile, err := os.OpenFile(lf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			log.DefaultLogger.Logger.Out = logFile
			*(co.configKey.(*string)) = lf
		} else {
			stdLog.Fatal("Failed to log to file")
		}
	}
}

// setRateLimit converts a command line rate limit, and sets rate and burst limiting in the final config.
func setRateLimit(co *configOption) {
	var rateLimit *throttled.RateQuota = nil
	perHourRateLimit := viper.GetInt(co.name)
	if perHourRateLimit != 0 {
		rateLimit = &throttled.RateQuota{
			MaxRate:  throttled.PerHour(perHourRateLimit),
			MaxBurst: 100,
		}
		*(co.configKey.(**throttled.RateQuota)) = rateLimit
	}
}

// incrementTLSFlag tracks TLS command line options for later validation, and stores the provided TLS value.
func incrementTLSFlag(co *configOption) {
	tls := viper.GetString(co.name)
	if tls != "" {
		tlsProvided++
		*(co.configKey.(*string)) = tls
	}
}

// validateTLS ensures that both a TLS cert and key are provided, if either is provided
func validateTLS(tlsProvided int) {
	if tlsProvided == 1 {
		stdLog.Fatal("Invalid TLS config: both key and cert must be configured")
	}
}

// TODO: Test all options
// TODO: Clean up original config custom code after verification
// TODO: Config = c
var configOpts = []configOption{
	configOption{name: "db-url", envVar: "DATABASE_URL", configKey: &c.DatabaseURL, flagDefault: "", required: true, usage: "horizon postgres database to connect with"},
	configOption{name: "stellar-core-db-url", envVar: "STELLAR_CORE_DATABASE_URL", configKey: &c.StellarCoreDatabaseURL, flagDefault: "", required: true, usage: "stellar-core postgres database to connect with"},
	configOption{name: "stellar-core-url", configKey: &c.StellarCoreURL, flagDefault: "", required: true, usage: "stellar-core to connect with (for http commands)"},
	configOption{name: "port", configKey: &c.Port, flagDefault: 8000, usage: "tcp port to listen on for http requests"},
	configOption{name: "max-db-connections", configKey: &c.MaxDBConnections, flagDefault: 20, usage: "max db connections (per DB), may need to be increased when responses are slow but DB CPU is normal"},
	configOption{name: "sse-update-frequency", configKey: &c.SSEUpdateFrequency, flagDefault: 5, customSetValue: setDuration, usage: "defines how often streams should check if there's a new ledger (in seconds), may need to increase in case of big number of streams"},
	configOption{name: "connection-timeout", configKey: &c.ConnectionTimeout, flagDefault: 55, customSetValue: setDuration, usage: "defines the timeout of connection after which 504 response will be sent or stream will be closed, if Horizon is behind a load balancer with idle connection timeout, this should be set to a few seconds less that idle timeout"},
	configOption{name: "per-hour-rate-limit", configKey: &c.RateLimit, flagDefault: 3600, customSetValue: setRateLimit, usage: "max count of requests allowed in a one hour period, by remote ip address"},
	configOption{name: "rate-limit-redis-key", configKey: &c.RateLimitRedisKey, flagDefault: "", usage: "redis key for storing rate limit data, useful when deploying a cluster of Horizons, ignored when redis-url is empty"},
	configOption{name: "redis-url", configKey: &c.RedisURL, flagDefault: "", usage: "redis to connect with, for rate limiting"},
	configOption{name: "friendbot-url", configKey: &c.FriendbotURL, flagDefault: "", customSetValue: setURL, usage: "friendbot service to redirect to"},
	configOption{name: "log-level", configKey: &c.LogLevel, flagDefault: "info", customSetValue: setLogLevel, usage: "minimum log severity (debug, info, warn, error) to log"},
	configOption{name: "log-file", configKey: &c.LogFile, flagDefault: "", customSetValue: setLogFile, usage: "Name of the file where logs will be saved (leave empty to send logs to stdout)"},
	configOption{name: "max-path-length", configKey: &c.MaxPathLength, flagDefault: uint(4), usage: "the maximum number of assets on the path in `/paths` endpoint"},
	configOption{name: "network-passphrase", configKey: &c.NetworkPassphrase, flagDefault: network.TestNetworkPassphrase, required: true, usage: "Override the network passphrase"},
	configOption{name: "sentry-dsn", configKey: &c.SentryDSN, flagDefault: "", usage: "Sentry URL to which panics and errors should be reported"},
	configOption{name: "loggly-token", configKey: &c.LogglyToken, flagDefault: "", usage: "Loggly token, used to configure log forwarding to loggly"},
	configOption{name: "loggly-tag", configKey: &c.LogglyTag, flagDefault: "horizon", usage: "Tag to be added to every loggly log event"},
	configOption{name: "tls-cert", configKey: &c.TLSCert, flagDefault: "", customSetValue: incrementTLSFlag, usage: "TLS certificate file to use for securing connections to horizon"},
	configOption{name: "tls-key", configKey: &c.TLSKey, flagDefault: "", customSetValue: incrementTLSFlag, usage: "TLS private key file to use for securing connections to horizon"},
	configOption{name: "ingest", configKey: &c.Ingest, flagDefault: false, usage: "causes this horizon process to ingest data from stellar-core into horizon's db"},
	configOption{name: "history-retention-count", configKey: &c.HistoryRetentionCount, flagDefault: uint(0), usage: "the minimum number of ledgers to maintain within horizon's history tables.  0 signifies an unlimited number of ledgers will be retained"},
	configOption{name: "history-stale-threshold", configKey: &c.StaleThreshold, flagDefault: uint(0), usage: "the maximum number of ledgers the history db is allowed to be out of date from the connected stellar-core db before horizon considers history stale"},
	configOption{name: "skip-cursor-update", configKey: &c.SkipCursorUpdate, flagDefault: false, usage: "causes the ingester to skip reporting the last imported ledger state to stellar-core"},
	configOption{name: "enable-asset-stats", configKey: &c.EnableAssetStats, flagDefault: false, usage: "enables asset stats during the ingestion and expose `/assets` endpoint,  Enabling it has a negative impact on CPU"},
}

func main() {
	rootCmd.Execute()
}

func init() {
	viper.SetDefault("port", 8000)
	viper.SetDefault("history-retention-count", 0)

	rootCmd = &cobra.Command{
		Use:   "horizon",
		Short: "client-facing api server for the stellar network",
		Long:  "client-facing api server for the stellar network",
		Run: func(cmd *cobra.Command, args []string) {
			initApp(cmd, args)
			app.Serve()
		},
	}

	for i := range configOpts {
		co := &configOpts[i]

		// Bind the command line and environment variable name
		if co.envVar == "" {
			co.envVar = strutils.KebabToConstantCase(co.name)
			viper.BindEnv(co.name, co.envVar)
		}

		// Initialise the persistent flags
		co.setFlag()
	}

	rootCmd.AddCommand(dbCmd)

	viper.BindPFlags(rootCmd.PersistentFlags())
}

func initApp(cmd *cobra.Command, args []string) *horizon.App {
	initConfig()

	var err error
	app, err = horizon.NewApp(config)

	if err != nil {
		stdLog.Fatal(err.Error())
	}

	return app
}

func initConfig() {
	// Check all required args were provided
	for i := range configOpts {
		co := &configOpts[i]
		co.require()
	}

	migrationsToApplyUp := schema.GetMigrationsUp(viper.GetString("db-url"))
	if len(migrationsToApplyUp) > 0 {
		stdLog.Printf(`There are %v migrations to apply in the "up" direction.`, len(migrationsToApplyUp))
		stdLog.Printf("The necessary migrations are: %v", migrationsToApplyUp)
		stdLog.Printf("A database migration is required to run this version (%v) of Horizon. Run \"horizon db migrate up\" to update your DB. Consult the Changelog (https://github.com/stellar/horizon/blob/master/CHANGELOG.md) for more information.", apkg.Version())
		os.Exit(1)
	}

	nMigrationsDown := schema.GetNumMigrationsDown(viper.GetString("db-url"))
	if nMigrationsDown > 0 {
		stdLog.Printf("A database migration DOWN to an earlier version of the schema is required to run this version (%v) of Horizon. Consult the Changelog (https://github.com/stellar/horizon/blob/master/CHANGELOG.md) for more information.", apkg.Version())
		stdLog.Printf("In order to migrate the database DOWN, using the HIGHEST version number of Horizon you have installed (not this binary), run \"horizon db migrate down %v\".", nMigrationsDown)
		os.Exit(1)
	}

	// Run validation checks
	for i := range configOpts {
		co := &configOpts[i]
		co.setValue(&c)
	}
	// Validate log level
	ll, err := logrus.ParseLevel(viper.GetString("log-level"))
	if err != nil {
		stdLog.Fatalf("Could not parse log-level: %v", viper.GetString("log-level"))
	}
	log.DefaultLogger.Level = ll

	validateTLS(tlsProvided)

	// Write to a log file, if a file name was provided
	lf := viper.GetString("log-file")
	if lf != "" {
		logFile, err := os.OpenFile(lf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			log.DefaultLogger.Logger.Out = logFile
		} else {
			stdLog.Fatal("Failed to log to file")
		}
	}

	// Ensure that both a TLS cert and key are provided, if either is provided
	cert, key := viper.GetString("tls-cert"), viper.GetString("tls-key")
	switch {
	case cert != "" && key == "":
		stdLog.Fatal("Invalid TLS config: key not configured")
	case cert == "" && key != "":
		stdLog.Fatal("Invalid TLS config: cert not configured")
	}

	// Validate the friendbotURL is a URL, if it was provided
	var friendbotURL *url.URL
	friendbotURLString := viper.GetString("friendbot-url")
	if friendbotURLString != "" {
		friendbotURL, err = url.Parse(friendbotURLString)
		if err != nil {
			stdLog.Fatalf("Unable to parse URL: %s/%v", friendbotURLString, err)
		}
	}

	// Set rate and burst limiting if provided
	var rateLimit *throttled.RateQuota = nil
	perHourRateLimit := viper.GetInt("per-hour-rate-limit")
	if perHourRateLimit != 0 {
		rateLimit = &throttled.RateQuota{
			MaxRate:  throttled.PerHour(perHourRateLimit),
			MaxBurst: 100,
		}
	}

	config = horizon.Config{
		DatabaseURL:            viper.GetString("db-url"),
		StellarCoreDatabaseURL: viper.GetString("stellar-core-db-url"),
		StellarCoreURL:         viper.GetString("stellar-core-url"),
		Port:                   viper.GetInt("port"),
		MaxDBConnections:       viper.GetInt("max-db-connections"),
		SSEUpdateFrequency:     time.Duration(viper.GetInt("sse-update-frequency")) * time.Second,
		ConnectionTimeout:      time.Duration(viper.GetInt("connection-timeout")) * time.Second,
		RateLimit:              rateLimit,
		RateLimitRedisKey:      viper.GetString("rate-limit-redis-key"),
		RedisURL:               viper.GetString("redis-url"),
		FriendbotURL:           friendbotURL,
		LogLevel:               ll,
		LogFile:                lf,
		MaxPathLength:          uint(viper.GetInt("max-path-length")),
		NetworkPassphrase:      viper.GetString("network-passphrase"),
		SentryDSN:              viper.GetString("sentry-dsn"),
		LogglyToken:            viper.GetString("loggly-token"),
		LogglyTag:              viper.GetString("loggly-tag"),
		TLSCert:                cert,
		TLSKey:                 key,
		Ingest:                 viper.GetBool("ingest"),
		HistoryRetentionCount:  uint(viper.GetInt("history-retention-count")),
		StaleThreshold:         uint(viper.GetInt("history-stale-threshold")),
		SkipCursorUpdate:       viper.GetBool("skip-cursor-update"),
		EnableAssetStats:       viper.GetBool("enable-asset-stats"),
	}

	// For testing purposes only
	//stdLog.Fatal(configOpts)
	stdLog.Printf("DatabaseURL    \"%s\"(%T)    \"%s\"(%T)", c.DatabaseURL, c.DatabaseURL, config.DatabaseURL, config.DatabaseURL)
	stdLog.Printf("StellarCoreDatabaseURL    \"%s\"(%T)    \"%s\"(%T)", c.StellarCoreDatabaseURL, c.StellarCoreDatabaseURL, config.StellarCoreDatabaseURL, config.StellarCoreDatabaseURL)
	stdLog.Printf("StellarCoreURL    \"%s\"(%T)    \"%s\"(%T)", c.StellarCoreURL, c.StellarCoreURL, config.StellarCoreURL, config.StellarCoreURL)
	stdLog.Printf("Port    \"%d\"(%T)    \"%d\"(%T)", c.Port, c.Port, config.Port, config.Port)
	stdLog.Printf("MaxDBConnections    \"%d\"(%T)    \"%d\"(%T)", c.MaxDBConnections, c.MaxDBConnections, config.MaxDBConnections, config.MaxDBConnections)
	stdLog.Printf("SSEUpdateFrequency    \"%d\"(%T)    \"%d\"(%T)", c.SSEUpdateFrequency, c.SSEUpdateFrequency, config.SSEUpdateFrequency, config.SSEUpdateFrequency)
	stdLog.Printf("ConnectionTimeout    \"%d\"(%T)    \"%d\"(%T)", c.ConnectionTimeout, c.ConnectionTimeout, config.ConnectionTimeout, config.ConnectionTimeout)
	stdLog.Printf("RateLimit    \"%+v\"(%T)    \"%+v\"(%T)", c.RateLimit, c.RateLimit, config.RateLimit, config.RateLimit)
	stdLog.Printf("RateLimitRedisKey    \"%s\"(%T)    \"%s\"(%T)", c.RateLimitRedisKey, c.RateLimitRedisKey, config.RateLimitRedisKey, config.RateLimitRedisKey)
	stdLog.Printf("RedisURL    \"%s\"(%T)    \"%s\"(%T)", c.RedisURL, c.RedisURL, config.RedisURL, config.RedisURL)
	stdLog.Printf("FriendbotURL    \"%s\"(%T)    \"%s\"(%T)", c.FriendbotURL, c.FriendbotURL, config.FriendbotURL, config.FriendbotURL)
	stdLog.Printf("LogLevel    \"%s\"(%T)    \"%s\"(%T)", c.LogLevel, c.LogLevel, config.LogLevel, config.LogLevel)
	stdLog.Printf("LogFile    \"%s\"(%T)    \"%s\"(%T)", c.LogFile, c.LogFile, config.LogFile, config.LogFile)
	stdLog.Printf("MaxPathLength    \"%d\"(%T)    \"%d\"(%T)", c.MaxPathLength, c.MaxPathLength, config.MaxPathLength, config.MaxPathLength)
	stdLog.Printf("NetworkPassphrase    \"%s\"(%T)    \"%s\"(%T)", c.NetworkPassphrase, c.NetworkPassphrase, config.NetworkPassphrase, config.NetworkPassphrase)
	stdLog.Printf("SentryDSN    \"%s\"(%T)    \"%s\"(%T)", c.SentryDSN, c.SentryDSN, config.SentryDSN, config.SentryDSN)
	stdLog.Printf("LogglyToken    \"%s\"(%T)    \"%s\"(%T)", c.LogglyToken, c.LogglyToken, config.LogglyToken, config.LogglyToken)
	stdLog.Printf("LogglyTag    \"%s\"(%T)    \"%s\"(%T)", c.LogglyTag, c.LogglyTag, config.LogglyTag, config.LogglyTag)
	stdLog.Printf("TLSCert    \"%s\"(%T)    \"%s\"(%T)", c.TLSCert, c.TLSCert, config.TLSCert, config.TLSCert)
	stdLog.Printf("TLSKey    \"%s\"(%T)    \"%s\"(%T)", c.TLSKey, c.TLSKey, config.TLSKey, config.TLSKey)
	stdLog.Printf("Ingest    \"%t\"(%T)    \"%t\"(%T)", c.Ingest, c.Ingest, config.Ingest, config.Ingest)
	stdLog.Printf("HistoryRetentionCount    \"%d\"(%T)    \"%d\"(%T)", c.HistoryRetentionCount, c.HistoryRetentionCount, config.HistoryRetentionCount, config.HistoryRetentionCount)
	stdLog.Printf("StaleThreshold    \"%d\"(%T)    \"%d\"(%T)", c.StaleThreshold, c.StaleThreshold, config.StaleThreshold, config.StaleThreshold)
	stdLog.Printf("SkipCursorUpdate    \"%t\"(%T)    \"%t\"(%T)", c.SkipCursorUpdate, c.SkipCursorUpdate, config.SkipCursorUpdate, config.SkipCursorUpdate)
	// stdLog.Fatalf("RateLimit %v (%T)", c.RateLimit, c.RateLimit)
	// stdLog.Fatal(c)
	stdLog.Fatal("Died here")
}
