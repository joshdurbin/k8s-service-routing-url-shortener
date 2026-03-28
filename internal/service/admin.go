package service

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	hraft "github.com/hashicorp/raft"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	adminv1 "github.com/joshdurbin/k8s-service-routing-url-shortener/gen/go/admin/v1"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/db"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/raftcluster"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/pkg"
)

func dialAdmin(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

const defaultPageSize = 50

// AdminServer implements adminv1.AdminServiceServer.
type AdminServer struct {
	adminv1.UnimplementedAdminServiceServer
	cluster *raftcluster.Cluster
	cfg     *pkg.Config
	log     zerolog.Logger
}

func NewAdminServer(cluster *raftcluster.Cluster, cfg *pkg.Config, log zerolog.Logger) *AdminServer {
	return &AdminServer{cluster: cluster, cfg: cfg, log: log}
}

// ---- GetRaftState ----

func (s *AdminServer) GetRaftState(_ context.Context, _ *adminv1.GetRaftStateRequest) (*adminv1.GetRaftStateResponse, error) {
	r := s.cluster.Raft()
	stats := r.Stats()

	leaderAddr, _ := r.LeaderWithID()
	// Convert leader IP address to DNS name for consistency with peer addresses
	leaderDNS := s.cluster.IPToDNSName(string(leaderAddr))

	cfgFuture := r.GetConfiguration()
	if err := cfgFuture.Error(); err != nil {
		return nil, status.Errorf(codes.Internal, "get configuration: %v", err)
	}

	var peers []*adminv1.RaftPeer
	for _, srv := range cfgFuture.Configuration().Servers {
		peers = append(peers, &adminv1.RaftPeer{
			Id:       string(srv.ID),
			Address:  string(srv.Address),
			Suffrage: srv.Suffrage.String(),
		})
	}

	term, _ := strconv.ParseUint(stats["term"], 10, 64)
	commitIndex, _ := strconv.ParseUint(stats["commit_index"], 10, 64)
	lastIndex, _ := strconv.ParseUint(stats["last_log_index"], 10, 64)

	return &adminv1.GetRaftStateResponse{
		Leader:      leaderDNS,
		State:       r.State().String(),
		Term:        term,
		CommitIndex: commitIndex,
		LastIndex:   lastIndex,
		Peers:       peers,
	}, nil
}

// ---- TriggerElection ----

func (s *AdminServer) TriggerElection(ctx context.Context, req *adminv1.TriggerElectionRequest) (*adminv1.TriggerElectionResponse, error) {
	if !s.cluster.IsLeader() {
		// Forward to leader.
		leaderAddr := s.cluster.LeaderAdminAddr()
		if leaderAddr == "" {
			return nil, status.Error(codes.Unavailable, "no leader available")
		}
		conn, err := dialAdmin(ctx, leaderAddr)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "dial leader %s: %v", leaderAddr, err)
		}
		defer conn.Close()
		return adminv1.NewAdminServiceClient(conn).TriggerElection(ctx, req)
	}
	if err := s.cluster.Raft().LeadershipTransfer().Error(); err != nil {
		return &adminv1.TriggerElectionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &adminv1.TriggerElectionResponse{Ok: true, Message: "leadership transfer initiated"}, nil
}

// ---- ListURLs ----

func (s *AdminServer) ListURLs(ctx context.Context, req *adminv1.ListURLsRequest) (*adminv1.ListURLsResponse, error) {
	pageSize := int64(req.PageSize)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	afterID := int64(0)
	if req.PageToken != "" {
		var err error
		afterID, err = strconv.ParseInt(req.PageToken, 10, 64)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page_token")
		}
	}

	rows, err := s.cluster.Queries().ListURLs(ctx, db.ListURLsParams{
		ID:    afterID,
		Limit: pageSize + 1, // fetch one extra to detect next page
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list urls: %v", err)
	}

	nextPageToken := ""
	if int64(len(rows)) > pageSize {
		rows = rows[:pageSize]
		nextPageToken = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}

	entries := make([]*adminv1.URLEntry, 0, len(rows))
	for _, u := range rows {
		entries = append(entries, &adminv1.URLEntry{
			ShortCode: u.ShortCode,
			LongUrl:   u.LongUrl,
			CreatedAt: timestamppb.New(time.Unix(u.CreatedAt, 0)),
		})
	}

	return &adminv1.ListURLsResponse{
		Urls:          entries,
		NextPageToken: nextPageToken,
	}, nil
}

// ---- ListURLsWithStats ----

func (s *AdminServer) ListURLsWithStats(ctx context.Context, req *adminv1.ListURLsWithStatsRequest) (*adminv1.ListURLsWithStatsResponse, error) {
	pageSize := int64(req.PageSize)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	afterID := int64(0)
	if req.PageToken != "" {
		var err error
		afterID, err = strconv.ParseInt(req.PageToken, 10, 64)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page_token")
		}
	}

	rows, err := s.cluster.Queries().ListURLsWithStats(ctx, db.ListURLsWithStatsParams{
		ID:    afterID,
		Limit: pageSize + 1,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list urls with stats: %v", err)
	}

	totalCount, err := s.cluster.Queries().CountURLs(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count urls: %v", err)
	}

	nextPageToken := ""
	if int64(len(rows)) > pageSize {
		rows = rows[:pageSize]
		nextPageToken = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}

	entries := make([]*adminv1.URLEntryWithStats, 0, len(rows))
	for _, u := range rows {
		e := &adminv1.URLEntryWithStats{
			ShortCode:   u.ShortCode,
			LongUrl:     u.LongUrl,
			CreatedAt:   timestamppb.New(time.Unix(u.CreatedAt, 0)),
			FollowCount: u.FollowCount,
		}
		if u.FirstFollow.Valid {
			e.FirstFollow = timestamppb.New(time.Unix(u.FirstFollow.Int64, 0))
		}
		if u.LastFollow.Valid {
			e.LastFollow = timestamppb.New(time.Unix(u.LastFollow.Int64, 0))
		}
		entries = append(entries, e)
	}

	return &adminv1.ListURLsWithStatsResponse{
		Urls:          entries,
		NextPageToken: nextPageToken,
		TotalCount:    totalCount,
		PageSize:      int32(pageSize),
	}, nil
}

// ---- DeleteURL ----

func (s *AdminServer) DeleteURL(ctx context.Context, req *adminv1.DeleteURLRequest) (*adminv1.DeleteURLResponse, error) {
	if req.ShortCode == "" {
		return nil, status.Error(codes.InvalidArgument, "short_code is required")
	}
	if !s.cluster.IsLeader() {
		// Forward to leader.
		leaderAddr := s.cluster.LeaderAdminAddr()
		conn, err := dialAdmin(ctx, leaderAddr)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "dial leader %s: %v", leaderAddr, err)
		}
		defer conn.Close()
		resp, err := adminv1.NewAdminServiceClient(conn).DeleteURL(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}
	result, err := s.cluster.Apply(raftcluster.CmdDeleteURL, raftcluster.DeleteURLPayload{
		ShortCode: req.ShortCode,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "raft apply: %v", err)
	}
	if result.Err != nil {
		return nil, status.Errorf(codes.Internal, "fsm apply: %v", result.Err)
	}
	return &adminv1.DeleteURLResponse{Ok: true}, nil
}

// ---- GetCounterState ----

func (s *AdminServer) GetCounterState(ctx context.Context, _ *adminv1.GetCounterStateRequest) (*adminv1.GetCounterStateResponse, error) {
	ctr, err := s.cluster.Queries().GetCounter(ctx)
	if err != nil && err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "get counter: %v", err)
	}
	start, end, next := s.cluster.ReservedRange()
	return &adminv1.GetCounterStateResponse{
		PersistedValue: ctr.Value,
		ReservedStart:  start,
		ReservedEnd:    end,
		NextToIssue:    next,
	}, nil
}

// ---- GetFollowStats ----

func (s *AdminServer) GetFollowStats(ctx context.Context, req *adminv1.GetFollowStatsRequest) (*adminv1.GetFollowStatsResponse, error) {
	if req.ShortCode == "" {
		return nil, status.Error(codes.InvalidArgument, "short_code is required")
	}
	stat, err := s.cluster.Queries().GetFollowStats(ctx, req.ShortCode)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, status.Errorf(codes.NotFound, "no stats for %s", req.ShortCode)
		}
		return nil, status.Errorf(codes.Internal, "get follow stats: %v", err)
	}

	resp := &adminv1.GetFollowStatsResponse{
		ShortCode:   stat.ShortCode,
		FollowCount: stat.FollowCount,
	}
	if stat.FirstFollow.Valid {
		resp.FirstFollow = timestamppb.New(time.Unix(stat.FirstFollow.Int64, 0))
	}
	if stat.LastFollow.Valid {
		resp.LastFollow = timestamppb.New(time.Unix(stat.LastFollow.Int64, 0))
	}
	return resp, nil
}

// ---- JoinCluster (internal — called by new pods on startup) ----

func (s *AdminServer) JoinCluster(_ context.Context, req *adminv1.JoinClusterRequest) (*adminv1.JoinClusterResponse, error) {
	if !s.cluster.IsLeader() {
		return &adminv1.JoinClusterResponse{
			Ok:      false,
			Message: fmt.Sprintf("not the leader; try %s", s.cluster.LeaderAdminAddr()),
		}, nil
	}
	f := s.cluster.Raft().AddVoter(
		hraft.ServerID(req.NodeId),
		hraft.ServerAddress(req.RaftAddr),
		0, s.cfg.RaftApplyTimeout,
	)
	if err := f.Error(); err != nil {
		return &adminv1.JoinClusterResponse{Ok: false, Message: err.Error()}, nil
	}
	s.log.Info().Str("node_id", req.NodeId).Str("raft_addr", req.RaftAddr).Msg("node joined cluster")
	return &adminv1.JoinClusterResponse{Ok: true}, nil
}

// ---- RecordFollow (internal — called by followers via SyncRecorder/AsyncRecorder) ----

func (s *AdminServer) RecordFollow(ctx context.Context, req *adminv1.RecordFollowRequest) (*adminv1.RecordFollowResponse, error) {
	if !s.cluster.IsLeader() {
		return nil, status.Errorf(codes.FailedPrecondition, "not the leader")
	}
	result, err := s.cluster.Apply(raftcluster.CmdRecordFollow, raftcluster.RecordFollowPayload{
		ShortCode: req.ShortCode,
		At:        req.At,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "raft apply: %v", err)
	}
	if result.Err != nil {
		return nil, status.Errorf(codes.Internal, "fsm apply: %v", result.Err)
	}
	return &adminv1.RecordFollowResponse{Ok: true}, nil
}
