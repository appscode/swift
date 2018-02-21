package server

import (
	"net/http"
	"strings"

	stringz "github.com/appscode/go/strings"
	utilerrors "github.com/appscode/go/util/errors"
	grpc_cors "github.com/appscode/grpc-go-addons/cors"
	"github.com/appscode/grpc-go-addons/endpoints"
	grpc_security "github.com/appscode/grpc-go-addons/security"
	"github.com/appscode/grpc-go-addons/server"
	"github.com/appscode/grpc-go-addons/server/options"
	proto "github.com/appscode/swift/pkg/apis/swift/v2"
	"github.com/appscode/swift/pkg/connectors"
	"github.com/appscode/swift/pkg/extpoints"
	"github.com/appscode/swift/pkg/release"
	"github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	"github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	"github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	gwrt "github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type SwiftOptions struct {
	RecommendedOptions *options.RecommendedOptions
	TillerOptions      *TillerOptions
	LogRPC             bool
}

func NewSwiftOptions() *SwiftOptions {
	o := &SwiftOptions{
		RecommendedOptions: options.NewRecommendedOptions(),
		TillerOptions:      NewTillerOptions(),
	}
	o.RecommendedOptions.SecureServing.PlaintextAddr = ":9855"
	o.RecommendedOptions.SecureServing.SecureAddr = ":50055"
	return o
}

func (o *SwiftOptions) AddFlags(fs *pflag.FlagSet) {
	o.RecommendedOptions.AddFlags(fs)
	o.TillerOptions.AddFlags(fs)
	fs.BoolVar(&o.LogRPC, "log-rpc", o.LogRPC, "log RPC request and response")
}

func (o SwiftOptions) Validate(args []string) error {
	var errors []error
	errors = append(errors, o.RecommendedOptions.Validate()...)
	return utilerrors.NewAggregate(errors)
}

func (o *SwiftOptions) Complete() error {
	return nil
}

func (o SwiftOptions) Config() (*server.Config, error) {
	config := server.NewConfig()
	if err := o.RecommendedOptions.ApplyTo(config); err != nil {
		return nil, err
	}

	extpoints.Connectors.Register(&connectors.InClusterConnector{
		TillerCACertFile:     o.TillerOptions.CACertFile,
		TillerClientCertFile: o.TillerOptions.ClientCertFile,
		TillerClientKeyFile:  o.TillerOptions.ClientPrivateKeyFile,
		InsecureSkipVerify:   o.TillerOptions.InsecureSkipVerify,
		Timeout:              o.TillerOptions.Timeout,
	}, connectors.UIDInClusterConnector)

	extpoints.Connectors.Register(&connectors.DirectConnector{
		TillerEndpoint:       o.TillerOptions.TillerEndpoint,
		TillerCACertFile:     o.TillerOptions.CACertFile,
		TillerClientCertFile: o.TillerOptions.ClientCertFile,
		TillerClientKeyFile:  o.TillerOptions.ClientPrivateKeyFile,
		InsecureSkipVerify:   o.TillerOptions.InsecureSkipVerify,
		Timeout:              o.TillerOptions.Timeout,
	}, connectors.UIDDirectConnector)

	extpoints.Connectors.Register(&connectors.KubeconfigConnector{
		Context:            o.TillerOptions.KubeContext,
		InsecureSkipVerify: o.TillerOptions.InsecureSkipVerify,
		Timeout:            o.TillerOptions.Timeout,
	}, connectors.UIDKubeconfigConnector)

	clientFactory := extpoints.Connectors.Lookup(o.TillerOptions.Connector)
	if clientFactory == nil {
		return nil, errors.New("failed to detect connector")
	}

	grpcRegistry := endpoints.GRPCRegistry{}
	grpcRegistry.Register(proto.RegisterReleaseServiceServer, &release.Server{ClientFactory: clientFactory})
	config.SetGRPCRegistry(grpcRegistry)

	gwRegistry := endpoints.ProxyRegistry{}
	gwRegistry.Register(proto.RegisterReleaseServiceHandlerFromEndpoint)
	config.SetProxyRegistry(gwRegistry)

	corsRegistry := grpc_cors.PatternRegistry{}
	corsRegistry.Register(proto.ExportReleaseServiceCorsPatterns())
	config.SetCORSRegistry(corsRegistry)

	optsLogrus := []grpc_logrus.Option{
		grpc_logrus.WithDecider(func(methodFullName string, err error) bool {
			return o.LogRPC
		}),
	}
	logrusEntry := logrus.NewEntry(logrus.New())
	grpc_logrus.ReplaceGrpcLogger(logrusEntry)

	config.GRPCServerOption(
		grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(
			grpc_ctxtags.StreamServerInterceptor(),
			grpc_opentracing.StreamServerInterceptor(),
			grpc_prometheus.StreamServerInterceptor,
			grpc_logrus.StreamServerInterceptor(logrusEntry, optsLogrus...),
			grpc_cors.StreamServerInterceptor(grpc_cors.OriginHost(config.CORSOriginHost), grpc_cors.AllowSubdomain(config.CORSAllowSubdomain)),
			grpc_security.StreamServerInterceptor(),
			grpc_recovery.StreamServerInterceptor(),
		)),
		grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			grpc_ctxtags.UnaryServerInterceptor(),
			grpc_opentracing.UnaryServerInterceptor(),
			grpc_prometheus.UnaryServerInterceptor,
			grpc_logrus.UnaryServerInterceptor(logrusEntry, optsLogrus...),
			grpc_cors.UnaryServerInterceptor(grpc_cors.OriginHost(config.CORSOriginHost), grpc_cors.AllowSubdomain(config.CORSAllowSubdomain)),
			grpc_security.UnaryServerInterceptor(),
			grpc_recovery.UnaryServerInterceptor(),
		)),
	)

	config.GatewayMuxOption(
		gwrt.WithIncomingHeaderMatcher(func(h string) (string, bool) {
			if stringz.PrefixFold(h, "access-control-request-") ||
				stringz.PrefixFold(h, "k8s-") ||
				strings.EqualFold(h, "Origin") ||
				strings.EqualFold(h, "Cookie") ||
				strings.EqualFold(h, "X-Phabricator-Csrf") {
				return h, true
			}
			return "", false
		}),
		gwrt.WithOutgoingHeaderMatcher(func(h string) (string, bool) {
			if stringz.PrefixFold(h, "access-control-allow-") ||
				strings.EqualFold(h, "Set-Cookie") ||
				strings.EqualFold(h, "vary") ||
				strings.EqualFold(h, "x-content-type-options") ||
				stringz.PrefixFold(h, "x-ratelimit-") {
				return h, true
			}
			return "", false
		}),
		gwrt.WithMetadata(func(c context.Context, req *http.Request) metadata.MD {
			return metadata.Pairs("x-forwarded-method", req.Method)
		}),
		gwrt.WithProtoErrorHandler(gwrt.DefaultHTTPProtoErrorHandler),
	)

	return config, nil
}

func (o SwiftOptions) RunServer(stopCh <-chan struct{}) error {
	config, err := o.Config()
	if err != nil {
		return err
	}

	server, err := config.New()
	if err != nil {
		return err
	}

	return server.Run(stopCh)
}