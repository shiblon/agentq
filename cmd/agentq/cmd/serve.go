package cmd

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/shiblon/entroq/pkg/backend/eqmem"
	"github.com/shiblon/entroq/pkg/eqsvcgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	pb "github.com/shiblon/entroq/api"
	hpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/health"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start an in-memory entroq gRPC server",
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().Int("port", 37706, "gRPC port to listen on")
	serveCmd.Flags().String("journal", "/data/agentq/journal", "Journal directory for persistence (empty disables journaling)")
	serveCmd.Flags().Bool("mkdir", true, "Create the journal directory if it does not exist")
	viper.BindPFlag("serve_port", serveCmd.Flags().Lookup("port"))
	viper.BindPFlag("serve_journal", serveCmd.Flags().Lookup("journal"))
	viper.BindPFlag("serve_mkdir", serveCmd.Flags().Lookup("mkdir"))
}

func runServe(cmd *cobra.Command, args []string) error {
	port := viper.GetInt("serve_port")
	journalDir := viper.GetString("serve_journal")
	mkdir := viper.GetBool("serve_mkdir")

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", port, err)
	}

	var memOpts []eqmem.Option
	if journalDir != "" {
		if mkdir {
			if err := os.MkdirAll(journalDir, 0700); err != nil {
				return fmt.Errorf("create journal dir %q: %w", journalDir, err)
			}
		}
		memOpts = append(memOpts, eqmem.WithJournal(journalDir))
		log.Printf("journaling to %s", journalDir)
	} else {
		log.Printf("warning: no journal directory set -- state is ephemeral")
	}

	ctx := cmd.Context()
	svc, err := eqsvcgrpc.New(ctx, eqmem.Opener(memOpts...))
	if err != nil {
		return fmt.Errorf("create eqmem service: %w", err)
	}
	defer svc.Close()

	s := grpc.NewServer()
	pb.RegisterEntroQServer(s, svc)
	hpb.RegisterHealthServer(s, health.NewServer())

	log.Printf("entroq gRPC server listening on :%d", port)
	return s.Serve(lis)
}
