package server

import (
	"net/http"
	"strings"

	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_glog "github.com/grpc-ecosystem/go-grpc-middleware/logging/glog"
	grpc_recovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	ctx_glog "github.com/grpc-ecosystem/go-grpc-middleware/tags/glog"
	grpc_opentracing "github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	gwrt "github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"golang.org/x/net/context"
	utilerrors "gomodules.xyz/errors"
	grpc_cors "gomodules.xyz/grpc-go-addons/cors"
	"gomodules.xyz/grpc-go-addons/endpoints"
	grpc_security "gomodules.xyz/grpc-go-addons/security"
	"gomodules.xyz/grpc-go-addons/server"
	"gomodules.xyz/grpc-go-addons/server/options"
	stringz "gomodules.xyz/x/strings"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	proto "kubepack.dev/swift/pkg/apis/swift/v2"
	"kubepack.dev/swift/pkg/connectors"
	"kubepack.dev/swift/pkg/extpoints"
	"kubepack.dev/swift/pkg/release"
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
	var errs []error
	errs = append(errs, o.RecommendedOptions.Validate()...)
	return utilerrors.NewAggregate(errs)
}

func (o *SwiftOptions) Complete() error {
	return nil
}

func (o SwiftOptions) Config() (*server.Config, error) {
	config := server.NewConfig()
	if err := o.RecommendedOptions.ApplyTo(config); err != nil {
		return nil, err
	}

	cc := connectors.Config{
		Endpoint:             o.TillerOptions.Endpoint,
		CACertFile:           o.TillerOptions.CACertFile,
		ClientCertFile:       o.TillerOptions.ClientCertFile,
		ClientPrivateKeyFile: o.TillerOptions.ClientPrivateKeyFile,
		InsecureSkipVerify:   o.TillerOptions.InsecureSkipVerify,
		Timeout:              o.TillerOptions.Timeout,
		KubeContext:          o.TillerOptions.KubeContext,
		LogRPC:               o.LogRPC,
	}
	extpoints.Connectors.Register(connectors.NewInClusterConnector(cc), connectors.UIDInClusterConnector)
	extpoints.Connectors.Register(connectors.NewDirectConnector(cc), connectors.UIDDirectConnector)
	extpoints.Connectors.Register(connectors.NewKubeconfigConnector(cc), connectors.UIDKubeconfigConnector)

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

	optsGLog := []grpc_glog.Option{
		grpc_glog.WithDecider(func(methodFullName string, err error) bool {
			return o.LogRPC
		}),
	}
	payloadDecider := func(ctx context.Context, fullMethodName string, servingObject interface{}) bool {
		return o.LogRPC
	}
	glogEntry := ctx_glog.NewEntry(ctx_glog.Logger)
	grpc_glog.ReplaceGrpcLogger()

	config.GRPCServerOption(
		grpc.StreamInterceptor(grpc_middleware.ChainStreamServer(
			grpc_ctxtags.StreamServerInterceptor(),
			grpc_opentracing.StreamServerInterceptor(),
			grpc_prometheus.StreamServerInterceptor,
			grpc_glog.PayloadStreamServerInterceptor(glogEntry, payloadDecider),
			grpc_glog.StreamServerInterceptor(glogEntry, optsGLog...),
			grpc_cors.StreamServerInterceptor(grpc_cors.OriginHost(config.CORSOriginHost), grpc_cors.AllowSubdomain(config.CORSAllowSubdomain)),
			grpc_security.StreamServerInterceptor(),
			grpc_recovery.StreamServerInterceptor(),
		)),
		grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			grpc_ctxtags.UnaryServerInterceptor(),
			grpc_opentracing.UnaryServerInterceptor(),
			grpc_prometheus.UnaryServerInterceptor,
			grpc_glog.PayloadUnaryServerInterceptor(glogEntry, payloadDecider),
			grpc_glog.UnaryServerInterceptor(glogEntry, optsGLog...),
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
