package raftcluster

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	hraft "github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	adminv1 "github.com/joshdurbin/k8s-service-routing-url-shortener/gen/go/admin/v1"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/db"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/pkg"

	// Register modernc sqlite driver.
	_ "modernc.org/sqlite"
)


// Cluster wraps hashicorp/raft and manages all node lifecycle concerns:
// bootstrap, dynamic peer discovery via K8s EndpointSlices, counter reservation,
// and pod label updates for Istio leader routing.
type Cluster struct {
	raft        *hraft.Raft
	transport   *hraft.NetworkTransport
	sqlDB       *sql.DB
	queries     *db.Queries
	metrics     *pkg.Metrics
	log         zerolog.Logger
	cfg         *pkg.Config
	k8sClient   kubernetes.Interface // nil when not running in-cluster
	nodeID      string
	raftAddr    string

	// Counter reservation — guarded by mu, used only on the leader.
	mu            sync.Mutex
	reservedStart int64
	reservedEnd   int64
	nextToIssue   int64

	needsJoin bool
}

// New opens all storage backends, wires up hashicorp/raft, and returns a
// ready-to-run Cluster. Call Run(ctx) to start background goroutines.
func New(cfg *pkg.Config, metrics *pkg.Metrics, log zerolog.Logger) (*Cluster, error) {
	if err := os.MkdirAll(cfg.RaftDataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create raft data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SQLitePath), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite dir: %w", err)
	}

	// ---- SQLite ----
	dsn := cfg.SQLitePath + "?_journal=WAL&_timeout=5000"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// WAL mode: one writer, multiple readers. Keep a single open connection
	// for the writer path so SQLite's mutex is never contended.
	sqlDB.SetMaxOpenConns(1)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, err
	}
	goose.SetBaseFS(db.MigrationsFS)
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	queries := db.New(sqlDB)

	// ---- BoltDB log + stable stores (single file) ----
	boltStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.RaftDataDir, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("open boltdb: %w", err)
	}

	// ---- File snapshot store ----
	snapStore, err := hraft.NewFileSnapshotStore(cfg.RaftDataDir, 3, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("create snapshot store: %w", err)
	}

	// ---- TCP transport ----
	raftAddr := podRaftAddr(cfg)
	bindAddr := fmt.Sprintf("0.0.0.0:%d", cfg.RaftPort)
	advertise, err := net.ResolveTCPAddr("tcp", raftAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve raft advertise addr %s: %w", raftAddr, err)
	}
	transport, err := hraft.NewTCPTransport(bindAddr, advertise, 3, cfg.RaftApplyTimeout, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("create raft transport: %w", err)
	}

	// ---- FSM ----
	fsm := newFSM(sqlDB, queries, metrics, log)

	// ---- Raft config ----
	raftCfg := hraft.DefaultConfig()
	raftCfg.LocalID = hraft.ServerID(raftAddr)
	raftCfg.Logger = hclog.New(&hclog.LoggerOptions{
		Name:   "raft",
		Level:  hclog.Warn,
		Output: os.Stderr,
	})
	raftCfg.HeartbeatTimeout = cfg.RaftHeartbeatTimeout
	raftCfg.ElectionTimeout = cfg.RaftElectionTimeout
	raftCfg.CommitTimeout = cfg.RaftCommitTimeout
	raftCfg.MaxAppendEntries = cfg.RaftMaxAppendEntries
	raftCfg.TrailingLogs = cfg.RaftTrailingLogs
	raftCfg.SnapshotInterval = cfg.RaftSnapshotInterval
	raftCfg.SnapshotThreshold = cfg.RaftSnapshotThreshold

	// ---- Bootstrap or join? ----
	hasState, err := hraft.HasExistingState(boltStore, boltStore, snapStore)
	if err != nil {
		return nil, fmt.Errorf("check raft state: %w", err)
	}

	ordinal, err := podOrdinal(cfg.PodName)
	if err != nil {
		return nil, fmt.Errorf("parse pod ordinal from %q: %w", cfg.PodName, err)
	}

	needsJoin := false
	if !hasState {
		if ordinal == 0 {
			bootstrapCfg := hraft.Configuration{
				Servers: []hraft.Server{
					{ID: hraft.ServerID(raftAddr), Address: hraft.ServerAddress(raftAddr)},
				},
			}
			if err := hraft.BootstrapCluster(raftCfg, boltStore, boltStore, snapStore, transport, bootstrapCfg); err != nil {
				return nil, fmt.Errorf("bootstrap cluster: %w", err)
			}
			log.Info().Str("raft_addr", raftAddr).Msg("bootstrapped single-node raft cluster")
		} else {
			needsJoin = true
		}
	}

	r, err := hraft.NewRaft(raftCfg, fsm, boltStore, boltStore, snapStore, transport)
	if err != nil {
		return nil, fmt.Errorf("create raft: %w", err)
	}

	// ---- K8s client (best-effort; absent outside the cluster) ----
	var k8sClient kubernetes.Interface
	if k8sCfg, err := rest.InClusterConfig(); err == nil {
		if cs, err := kubernetes.NewForConfig(k8sCfg); err == nil {
			k8sClient = cs
		}
	} else {
		log.Warn().Msg("not running in-cluster — K8s peer discovery and label updates disabled")
	}

	return &Cluster{
		raft:      r,
		transport: transport,
		sqlDB:     sqlDB,
		queries:   queries,
		metrics:   metrics,
		log:       log,
		cfg:       cfg,
		k8sClient: k8sClient,
		nodeID:    raftAddr,
		raftAddr:  raftAddr,
		needsJoin: needsJoin,
	}, nil
}

// Run blocks until ctx is cancelled. Must be called in an errgroup goroutine.
func (c *Cluster) Run(ctx context.Context) error {
	if c.needsJoin {
		go c.joinExistingCluster(ctx)
	}
	if c.k8sClient != nil {
		go c.peerReconcileLoop(ctx)
		go c.leaderLabelLoop(ctx)
	}
	<-ctx.Done()
	c.log.Info().Msg("shutting down raft")
	if err := c.raft.Shutdown().Error(); err != nil {
		return fmt.Errorf("raft shutdown: %w", err)
	}
	return c.sqlDB.Close()
}

// ---- Counter reservation ----

// NextCounter returns the next counter value for short code generation.
// Must only be called on the leader.
func (c *Cluster) NextCounter(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.nextToIssue >= c.reservedEnd {
		if err := c.reserveBlock(ctx); err != nil {
			return 0, err
		}
	}
	v := c.nextToIssue
	c.nextToIssue++
	return v, nil
}

func (c *Cluster) reserveBlock(ctx context.Context) error {
	ctr, err := c.queries.GetCounter(ctx)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("get counter: %w", err)
	}
	current := ctr.Value
	next := current + c.cfg.CounterBlockSize

	res, err := c.Apply(CmdReserveBlock, ReserveBlockPayload{NewCounterValue: next})
	if err != nil {
		return fmt.Errorf("reserve block via raft: %w", err)
	}
	if res.Err != nil {
		return res.Err
	}
	c.reservedStart = current
	c.reservedEnd = next
	c.nextToIssue = current
	c.log.Debug().Int64("start", current).Int64("end", next).Msg("reserved counter block")
	return nil
}

// ---- Raft apply ----

// Apply encodes a command and blocks until committed on a quorum.
func (c *Cluster) Apply(t CommandType, payload interface{}) (*ApplyResult, error) {
	b, err := marshalCommand(t, payload)
	if err != nil {
		return nil, err
	}
	f := c.raft.Apply(b, c.cfg.RaftApplyTimeout)
	if err := f.Error(); err != nil {
		return nil, err
	}
	result, ok := f.Response().(*ApplyResult)
	if !ok {
		return &ApplyResult{}, nil
	}
	return result, nil
}

// ---- Accessors ----

func (c *Cluster) IsLeader() bool {
	return c.raft.State() == hraft.Leader
}

func (c *Cluster) LeaderAddr() string {
	addr, _ := c.raft.LeaderWithID()
	return string(addr)
}

func (c *Cluster) Raft() *hraft.Raft      { return c.raft }
func (c *Cluster) DB() *sql.DB            { return c.sqlDB }
func (c *Cluster) Queries() *db.Queries   { return c.queries }

// LeaderAdminAddr returns the admin gRPC address of the current raft leader,
// derived by replacing the raft TCP port with the admin gRPC port.
func (c *Cluster) LeaderAdminAddr() string {
	leaderRaftAddr := c.LeaderAddr()
	if leaderRaftAddr == "" {
		return ""
	}
	return raftAddrToAdminAddr(leaderRaftAddr, c.cfg.GRPCAdminPort)
}

func (c *Cluster) ReservedRange() (start, end, next int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reservedStart, c.reservedEnd, c.nextToIssue
}

// ---- Join existing cluster ----

func (c *Cluster) joinExistingCluster(ctx context.Context) {
	c.log.Info().Msg("joining existing raft cluster")
	for attempt := 0; attempt < c.cfg.RaftJoinMaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := c.tryJoin(ctx); err != nil {
			c.log.Warn().Err(err).Int("attempt", attempt+1).Msg("join failed, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.cfg.RaftJoinRetryInterval):
			}
			continue
		}
		c.log.Info().Msg("successfully joined raft cluster")
		return
	}
	c.log.Error().Msg("exhausted join retries — node will remain as a non-voter")
}

func (c *Cluster) tryJoin(ctx context.Context) error {
	peers := c.knownPeers()
	if len(peers) == 0 {
		return fmt.Errorf("no peers discovered yet")
	}

	leaderAdmin := ""
	for _, adminAddr := range peers {
		conn, err := grpc.NewClient(adminAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			continue
		}
		resp, err := adminv1.NewAdminServiceClient(conn).GetRaftState(ctx, &adminv1.GetRaftStateRequest{})
		conn.Close()
		if err != nil {
			continue
		}
		if resp.State == "Leader" {
			leaderAdmin = adminAddr
			break
		}
		if resp.Leader != "" {
			leaderAdmin = raftAddrToAdminAddr(resp.Leader, c.cfg.GRPCAdminPort)
			break
		}
	}
	if leaderAdmin == "" {
		return fmt.Errorf("could not locate leader among peers %v", peers)
	}

	conn, err := grpc.NewClient(leaderAdmin, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial leader admin %s: %w", leaderAdmin, err)
	}
	defer conn.Close()

	resp, err := adminv1.NewAdminServiceClient(conn).JoinCluster(ctx, &adminv1.JoinClusterRequest{
		NodeId:   c.nodeID,
		RaftAddr: c.raftAddr,
	})
	if err != nil {
		return fmt.Errorf("JoinCluster RPC: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("JoinCluster refused: %s", resp.Message)
	}
	return nil
}

func (c *Cluster) knownPeers() []string {
	if c.k8sClient != nil {
		if peers, err := c.peersFromEndpointSlices(); err == nil && len(peers) > 0 {
			return peers
		}
	}
	return c.peersFromDNS()
}

func (c *Cluster) peersFromEndpointSlices() ([]string, error) {
	slices, err := c.k8sClient.DiscoveryV1().
		EndpointSlices(c.cfg.K8sNamespace).
		List(context.Background(), metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=" + c.cfg.K8sHeadlessService,
		})
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.TargetRef == nil || endpoint.TargetRef.Name == c.cfg.PodName {
				continue
			}
			addrs = append(addrs, fmt.Sprintf("%s.%s.%s.svc.cluster.local:%d",
				endpoint.TargetRef.Name, c.cfg.K8sHeadlessService,
				c.cfg.K8sNamespace, c.cfg.GRPCAdminPort))
		}
	}
	return addrs, nil
}

func (c *Cluster) peersFromDNS() []string {
	ordinal, _ := podOrdinal(c.cfg.PodName)
	prefix := podNamePrefix(c.cfg.PodName)
	var addrs []string
	for i := 0; i < 5; i++ {
		if i == ordinal {
			continue
		}
		addrs = append(addrs, fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local:%d",
			prefix, i, c.cfg.K8sHeadlessService, c.cfg.K8sNamespace, c.cfg.GRPCAdminPort))
	}
	return addrs
}

// ---- Peer reconciliation (leader only) ----

func (c *Cluster) peerReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.RaftReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.IsLeader() {
				c.reconcilePeers(ctx)
			}
		}
	}
}

func (c *Cluster) reconcilePeers(ctx context.Context) {
	slices, err := c.k8sClient.DiscoveryV1().
		EndpointSlices(c.cfg.K8sNamespace).
		List(ctx, metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=" + c.cfg.K8sHeadlessService,
		})
	if err != nil {
		c.log.Warn().Err(err).Msg("reconcile peers: list endpoint slices")
		return
	}

	liveAddrs := map[string]struct{}{}
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.TargetRef == nil {
				continue
			}
			ra := fmt.Sprintf("%s.%s.%s.svc.cluster.local:%d",
				endpoint.TargetRef.Name, c.cfg.K8sHeadlessService,
				c.cfg.K8sNamespace, c.cfg.RaftPort)
			liveAddrs[ra] = struct{}{}
		}
	}

	cfgFuture := c.raft.GetConfiguration()
	if err := cfgFuture.Error(); err != nil {
		c.log.Warn().Err(err).Msg("reconcile peers: get configuration")
		return
	}
	configured := map[string]struct{}{}
	for _, srv := range cfgFuture.Configuration().Servers {
		configured[string(srv.Address)] = struct{}{}
	}

	for ra := range liveAddrs {
		if _, ok := configured[ra]; !ok {
			c.log.Info().Str("peer", ra).Msg("adding new voter")
			if err := c.raft.AddVoter(hraft.ServerID(ra), hraft.ServerAddress(ra), 0, c.cfg.RaftApplyTimeout).Error(); err != nil {
				c.log.Warn().Err(err).Str("peer", ra).Msg("AddVoter failed")
			}
		}
	}

	for _, srv := range cfgFuture.Configuration().Servers {
		addr := string(srv.Address)
		if addr == c.raftAddr {
			continue
		}
		if _, ok := liveAddrs[addr]; !ok {
			c.log.Info().Str("peer", addr).Msg("removing departed voter")
			if err := c.raft.RemoveServer(srv.ID, 0, c.cfg.RaftApplyTimeout).Error(); err != nil {
				c.log.Warn().Err(err).Str("peer", addr).Msg("RemoveServer failed")
			}
		}
	}
}

// ---- Leader label updates for Istio routing ----

func (c *Cluster) leaderLabelLoop(ctx context.Context) {
	leaderCh := c.raft.LeaderCh()
	c.updateRaftRoleLabel(ctx, c.IsLeader())
	for {
		select {
		case <-ctx.Done():
			return
		case isLeader := <-leaderCh:
			c.updateRaftRoleLabel(ctx, isLeader)
		}
	}
}

func (c *Cluster) updateRaftRoleLabel(ctx context.Context, isLeader bool) {
	role := "follower"
	if isLeader {
		role = "leader"
		c.log.Info().Msg("this node became the raft leader")
		c.mu.Lock()
		c.reservedStart, c.reservedEnd, c.nextToIssue = 0, 0, 0
		c.mu.Unlock()
	} else {
		c.log.Info().Msg("this node became a raft follower")
	}
	if c.k8sClient == nil {
		return
	}
	patch := fmt.Sprintf(`{"metadata":{"labels":{"raft-role":%q}}}`, role)
	if _, err := c.k8sClient.CoreV1().Pods(c.cfg.K8sNamespace).
		Patch(ctx, c.cfg.PodName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		c.log.Warn().Err(err).Str("role", role).Msg("failed to update pod raft-role label")
	}
}

// ---- Helpers ----

func podRaftAddr(cfg *pkg.Config) string {
	return fmt.Sprintf("%s.%s.%s.svc.cluster.local:%d",
		cfg.PodName, cfg.K8sHeadlessService, cfg.K8sNamespace, cfg.RaftPort)
}

func podOrdinal(podName string) (int, error) {
	idx := strings.LastIndex(podName, "-")
	if idx < 0 {
		return 0, fmt.Errorf("no '-' in pod name %q", podName)
	}
	return strconv.Atoi(podName[idx+1:])
}

func podNamePrefix(podName string) string {
	idx := strings.LastIndex(podName, "-")
	if idx < 0 {
		return podName
	}
	return podName[:idx]
}

func raftAddrToAdminAddr(raftAddr string, adminPort int) string {
	host, _, err := net.SplitHostPort(raftAddr)
	if err != nil {
		return raftAddr
	}
	return net.JoinHostPort(host, strconv.Itoa(adminPort))
}

// IPToDNSName returns the DNS name for a raft peer given its IP address.
// It looks up the IP in the current raft configuration.
func (c *Cluster) IPToDNSName(ipAddr string) string {
	host, port, err := net.SplitHostPort(ipAddr)
	if err != nil {
		return ipAddr
	}

	// Check if it's already a DNS name (not an IP)
	if net.ParseIP(host) == nil {
		return ipAddr
	}

	// Look through the raft configuration for a matching IP
	cfgFuture := c.raft.GetConfiguration()
	if err := cfgFuture.Error(); err != nil {
		return ipAddr
	}

	for _, srv := range cfgFuture.Configuration().Servers {
		srvAddr := string(srv.Address)
		srvHost, srvPort, err := net.SplitHostPort(srvAddr)
		if err != nil || srvPort != port {
			continue
		}
		// Resolve the DNS name to see if it matches the IP
		ips, err := net.LookupIP(srvHost)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if ip.String() == host {
				return srvAddr
			}
		}
	}
	return ipAddr
}
