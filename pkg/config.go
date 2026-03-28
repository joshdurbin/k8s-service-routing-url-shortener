package pkg

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

// ShutdownTimeout is the maximum time allowed for graceful shutdown of each component.
const ShutdownTimeout = 5 * time.Second

type Config struct {
	// Ports
	HTTPPort       int `mapstructure:"http_port"`
	GRPCPublicPort int `mapstructure:"grpc_public_port"`
	GRPCAdminPort  int `mapstructure:"grpc_admin_port"`
	MetricsPort    int `mapstructure:"metrics_port"`
	RaftPort       int `mapstructure:"raft_port"`

	// PublicHost is used to build the short_url in ShortenURL responses.
	// Set to the externally visible hostname/IP (e.g. the Istio ingress gateway host).
	PublicHost string `mapstructure:"public_host"`

	// Storage
	RaftDataDir string `mapstructure:"raft_data_dir"`
	SQLitePath  string `mapstructure:"sqlite_path"`

	// URL shortener behaviour
	CounterBlockSize int64  `mapstructure:"counter_block_size"`
	ShortCodeXORKey  uint64 `mapstructure:"short_code_xor_key"`

	// Observability
	OTELEndpoint string `mapstructure:"otel_endpoint"`
	LogLevel     string `mapstructure:"log_level"`

	// Kubernetes — injected via Downward API or env
	PodName            string `mapstructure:"pod_name"`
	K8sNamespace       string `mapstructure:"k8s_namespace"`
	K8sHeadlessService string `mapstructure:"k8s_headless_service"`

	// Raft consensus tuning — passed directly to hashicorp/raft Config.
	// Defaults match hashicorp/raft's own defaults.
	RaftHeartbeatTimeout  time.Duration `mapstructure:"raft_heartbeat_timeout"`
	RaftElectionTimeout   time.Duration `mapstructure:"raft_election_timeout"`
	RaftCommitTimeout     time.Duration `mapstructure:"raft_commit_timeout"`
	RaftMaxAppendEntries  int           `mapstructure:"raft_max_append_entries"`
	RaftTrailingLogs      uint64        `mapstructure:"raft_trailing_logs"`
	RaftSnapshotInterval  time.Duration `mapstructure:"raft_snapshot_interval"`
	RaftSnapshotThreshold uint64        `mapstructure:"raft_snapshot_threshold"`

	// Cluster operation tuning — timeouts and retry behaviour.
	RaftApplyTimeout      time.Duration `mapstructure:"raft_apply_timeout"`
	RaftReconcileInterval time.Duration `mapstructure:"raft_reconcile_interval"`
	RaftJoinRetryInterval time.Duration `mapstructure:"raft_join_retry_interval"`
	RaftJoinMaxRetries    int           `mapstructure:"raft_join_max_retries"`
}

func LoadConfig() (*Config, error) {
	v := viper.New()

	// Ports & storage
	v.SetDefault("http_port", 8080)
	v.SetDefault("grpc_public_port", 9092)
	v.SetDefault("grpc_admin_port", 9090)
	v.SetDefault("metrics_port", 8081)
	v.SetDefault("raft_port", 9091)
	v.SetDefault("public_host", "localhost:8080")
	v.SetDefault("raft_data_dir", "/data/raft")
	v.SetDefault("sqlite_path", "/data/db.sqlite")

	// URL shortener behaviour
	v.SetDefault("counter_block_size", 100)
	v.SetDefault("short_code_xor_key", uint64(0xdeadbeefcafebabe))

	// Observability
	v.SetDefault("otel_endpoint", "otel-collector:4317")
	v.SetDefault("log_level", "info")

	// Kubernetes
	v.SetDefault("pod_name", "")
	v.SetDefault("k8s_namespace", "url-shortener")
	v.SetDefault("k8s_headless_service", "urlshortener-headless")

	// Raft consensus tuning (matches hashicorp/raft DefaultConfig)
	v.SetDefault("raft_heartbeat_timeout", time.Second)
	v.SetDefault("raft_election_timeout", time.Second)
	v.SetDefault("raft_commit_timeout", 50*time.Millisecond)
	v.SetDefault("raft_max_append_entries", 64)
	v.SetDefault("raft_trailing_logs", uint64(10240))
	v.SetDefault("raft_snapshot_interval", 120*time.Second)
	v.SetDefault("raft_snapshot_threshold", uint64(8192))

	// Cluster operation tuning
	v.SetDefault("raft_apply_timeout", 10*time.Second)
	v.SetDefault("raft_reconcile_interval", 15*time.Second)
	v.SetDefault("raft_join_retry_interval", 3*time.Second)
	v.SetDefault("raft_join_max_retries", 30)

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
