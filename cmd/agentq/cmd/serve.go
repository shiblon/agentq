package cmd

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/shiblon/entroq/pkg/backend/eqmem"
	"github.com/shiblon/entroq/pkg/eqsvcgrpc"
	"github.com/shiblon/entroq/pkg/eqsvcjson"
	"github.com/shiblon/entroq/pkg/otel"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	pb "github.com/shiblon/entroq/api"
	"google.golang.org/grpc/health"
	hpb "google.golang.org/grpc/health/grpc_health_v1"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start an in-memory entroq gRPC server",
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().Int("port", 37706, "gRPC port to listen on")
	serveCmd.Flags().Int("http-port", 9100, "HTTP port for /metrics and Connect JSON endpoints")
	serveCmd.Flags().String("journal", "/data/agentq/journal", "Journal directory for persistence (empty disables journaling)")
	serveCmd.Flags().Bool("mkdir", true, "Create the journal directory if it does not exist")
	viper.BindPFlag("serve_port", serveCmd.Flags().Lookup("port"))
	viper.BindPFlag("serve_http_port", serveCmd.Flags().Lookup("http-port"))
	viper.BindPFlag("serve_journal", serveCmd.Flags().Lookup("journal"))
	viper.BindPFlag("serve_mkdir", serveCmd.Flags().Lookup("mkdir"))
}

func runServe(cmd *cobra.Command, args []string) error {
	port := viper.GetInt("serve_port")
	httpPort := viper.GetInt("serve_http_port")
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

	mp, metricsHandler, stopMetrics, err := otel.NewPrometheusProvider()
	if err != nil {
		return fmt.Errorf("otel setup: %w", err)
	}
	defer stopMetrics()

	ctx := cmd.Context()
	svc, err := eqsvcgrpc.New(ctx, eqmem.Opener(append(memOpts, eqmem.WithMeterProvider(mp))...),
		eqsvcgrpc.WithMeterProvider(mp))
	if err != nil {
		return fmt.Errorf("create eqmem service: %w", err)
	}
	defer svc.Close()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metricsHandler)
		jsonPath, jsonHandler, err := eqsvcjson.New(svc)
		if err != nil {
			log.Fatalf("create JSON/Connect handler: %v", err)
		}
		mux.Handle(jsonPath, jsonHandler)
		log.Printf("entroq HTTP server (metrics + Connect JSON) listening on :%d", httpPort)
		log.Fatalf("http server: %v", http.ListenAndServe(fmt.Sprintf(":%d", httpPort), mux))
	}()

	s := grpc.NewServer()
	pb.RegisterEntroQServer(s, svc)
	hpb.RegisterHealthServer(s, health.NewServer())

	log.Printf("entroq gRPC server listening on :%d", port)
	return s.Serve(lis)
}
