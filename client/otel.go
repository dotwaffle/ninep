package client

import (
	"context"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	// "go.opentelemetry.io/otel/trace"
)

// instrumentationName is the OTel instrumentation scope name used for all
// tracers and meters created by this package.
const instrumentationName = "github.com/dotwaffle/ninep/client"

// otelInstruments holds all OTel metric instruments for a connection.
type otelInstruments struct {
	duration   metric.Float64Histogram
	reqSize    metric.Int64Counter
	respSize   metric.Int64Counter
	activeReqs metric.Int64UpDownCounter
}

func newOTelInstruments(mp metric.MeterProvider) *otelInstruments {
	meter := mp.Meter(instrumentationName)

	return &otelInstruments{
		duration: must(meter.Float64Histogram("ninep.client.duration",
			metric.WithUnit("s"),
			metric.WithDescription("Duration of 9P client operations"),
		)),
		reqSize: must(meter.Int64Counter("ninep.client.request.size",
			metric.WithUnit("By"),
			metric.WithDescription("Size of 9P request messages"),
		)),
		respSize: must(meter.Int64Counter("ninep.client.response.size",
			metric.WithUnit("By"),
			metric.WithDescription("Size of 9P response messages"),
		)),
		activeReqs: must(meter.Int64UpDownCounter("ninep.client.active_requests",
			metric.WithDescription("Number of active 9P requests"),
		)),
	}
}

// probeOTel populates c.tracerRecording and c.meterEnabled via a one-time probe
// of the configured providers.
func (c *Conn) probeOTel(cfg *config) {
	if cfg.tracerProvider != nil {
		tracer := cfg.tracerProvider.Tracer(instrumentationName)
		_, span := tracer.Start(context.Background(), "probe")
		c.tracerRecording = span.IsRecording()
		span.End()
	}

	if cfg.meterProvider != nil {
		meter := cfg.meterProvider.Meter(instrumentationName)
		counter, err := meter.Int64Counter("probe")
		if err == nil {
			c.meterEnabled = counter.Enabled(context.Background())
		}
	}
}

// buildOpNameAttrs returns a per-T-message-type metric.MeasurementOption map
// holding the rpc.method attribute.
func buildOpNameAttrs() map[proto.MessageType]metric.MeasurementOption {
	m := make(map[proto.MessageType]metric.MeasurementOption, len(requestMessageTypes))
	for _, t := range requestMessageTypes {
		m[t] = metric.WithAttributes(attribute.String("rpc.method", t.String()))
	}
	return m
}

// requestMessageTypes lists every T-message type the client may send.
var requestMessageTypes = [...]proto.MessageType{
	proto.TypeTversion,
	proto.TypeTauth,
	proto.TypeTattach,
	proto.TypeTflush,
	proto.TypeTwalk,
	proto.TypeTopen,
	proto.TypeTcreate,
	proto.TypeTread,
	proto.TypeTwrite,
	proto.TypeTclunk,
	proto.TypeTremove,
	proto.TypeTstat,
	proto.TypeTwstat,
	proto.TypeTstatfs,
	proto.TypeTlopen,
	proto.TypeTlcreate,
	proto.TypeTsymlink,
	proto.TypeTmknod,
	proto.TypeTrename,
	proto.TypeTreadlink,
	proto.TypeTgetattr,
	proto.TypeTsetattr,
	proto.TypeTxattrwalk,
	proto.TypeTxattrcreate,
	proto.TypeTreaddir,
	proto.TypeTfsync,
	proto.TypeTlock,
	proto.TypeTgetlock,
	proto.TypeTlink,
	proto.TypeTmkdir,
	proto.TypeTrenameat,
	proto.TypeTunlinkat,
}

// fidFromMessage extracts the primary Fid from a T-message.
func fidFromMessage(msg proto.Message) (proto.Fid, bool) {
	switch m := msg.(type) {
	// Shared base T-messages.
	case *proto.Tattach:
		return m.Fid, true
	case *proto.Twalk:
		return m.Fid, true
	case *proto.Tclunk:
		return m.Fid, true
	case *proto.Tread:
		return m.Fid, true
	case *proto.Twrite:
		return m.Fid, true
	case *proto.Tremove:
		return m.Fid, true

	// 9P2000.L T-messages.
	case *p9l.Tlopen:
		return m.Fid, true
	case *p9l.Tgetattr:
		return m.Fid, true
	case *p9l.Tsetattr:
		return m.Fid, true
	case *p9l.Treaddir:
		return m.Fid, true
	case *p9l.Tlcreate:
		return m.Fid, true
	case *p9l.Tmkdir:
		return m.DirFid, true
	case *p9l.Tsymlink:
		return m.DirFid, true
	case *p9l.Tlink:
		return m.DirFid, true
	case *p9l.Tmknod:
		return m.DirFid, true
	case *p9l.Treadlink:
		return m.Fid, true
	case *p9l.Tstatfs:
		return m.Fid, true
	case *p9l.Tfsync:
		return m.Fid, true
	case *p9l.Tunlinkat:
		return m.DirFid, true
	case *p9l.Trenameat:
		return m.OldDirFid, true
	case *p9l.Trename:
		return m.Fid, true
	case *p9l.Tlock:
		return m.Fid, true
	case *p9l.Tgetlock:
		return m.Fid, true
	case *p9l.Txattrwalk:
		return m.Fid, true
	case *p9l.Txattrcreate:
		return m.Fid, true

	// 9P2000.u T-messages.
	case *p9u.Topen:
		return m.Fid, true
	case *p9u.Tcreate:
		return m.Fid, true
	case *p9u.Tstat:
		return m.Fid, true
	case *p9u.Twstat:
		return m.Fid, true

	default:
		return 0, false
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic("otel instrument creation: " + err.Error())
	}
	return v
}
