package service

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	adminv1 "github.com/joshdurbin/k8s-service-routing-url-shortener/gen/go/admin/v1"
)

// RecordFollow records a URL follow event asynchronously by calling the
// admin gRPC service. Istio mesh routing ensures the request reaches the
// raft leader (via the leader subset).
func RecordFollow(adminAddr string, log zerolog.Logger, shortCode string, at time.Time) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn, err := grpc.NewClient(adminAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("record follow: dial failed")
			return
		}
		defer conn.Close()

		_, err = adminv1.NewAdminServiceClient(conn).RecordFollow(ctx, &adminv1.RecordFollowRequest{
			ShortCode: shortCode,
			At:        at.Unix(),
		})
		if err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("record follow: rpc failed")
		}
	}()
}
