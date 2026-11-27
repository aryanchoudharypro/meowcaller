// Package p2p is the direct (peer-to-peer) path of a 1:1 call, beside the
// relay, in WA Web's "web P2P" form: ICE, DTLS, then the wa-web-p2p SCTP
// data channel.
package p2p

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/fingerprint"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/sctp"
	"github.com/pion/stun/v4"
	"github.com/rs/zerolog"

	"github.com/purpshell/meowcaller/signaling"
)

// DataChannelLabel and DataChannelID name WA Web's P2P data channel.
const (
	DataChannelLabel = "wa-web-p2p"
	DataChannelID    = 0
)

// FallbackSTUNServer is the STUN server WA Web adds after the call's relays.
const FallbackSTUNServer = "edge-stun.facebook.com:3478"

// ConnectTimeout is how long WA Web waits for the path to open.
const ConnectTimeout = 30 * time.Second

const (
	maxSTUNServers         = 10
	maxBufferedCandidates  = 20
	certFingerprintAlgo    = "sha-256"
	candidatePriorityShift = 24
)

var (
	errClosed      = errors.New("p2p: connection closed")
	errNotOpen     = errors.New("p2p: data channel not open")
	errNoRemote    = errors.New("p2p: remote transport has no credentials")
	errFingerprint = errors.New("p2p: peer certificate does not match the signalled fingerprint")

	// ErrPeerRestart reports a new remote ufrag: the peer restarted ICE, and
	// like WA Web the whole connection is rebuilt (Close, then Dial again).
	ErrPeerRestart = errors.New("p2p: peer restarted ice")
)

// VoipSettings are the server call settings that gate P2P.
type VoipSettings struct {
	DisableP2P   bool
	EnableWebP2P bool
}

// Config describes one call's P2P attempt.
type Config struct {
	CallID      string
	IsCaller    bool
	PeerIsWeb   bool
	STUNServers []netip.AddrPort
	// SendTransport sends our type 13 transport to the peer. It is called
	// once with the credentials and again for each candidate found.
	SendTransport func(*signaling.WebP2PTransport) error
	// SkipFallbackSTUN leaves out FallbackSTUNServer (tests).
	SkipFallbackSTUN bool
	// IncludeLoopback gathers loopback candidates (tests).
	IncludeLoopback bool
}

// Conn is one direct path to the peer.
type Conn struct {
	cfg  Config
	log  zerolog.Logger
	cert tls.Certificate

	agent *ice.Agent
	ufrag string
	pwd   string
	fp    string

	mu         sync.Mutex
	candidates []signaling.WebP2PCandidate
	remoteSet  bool
	remoteUfrg string
	remotePwd  string
	remoteFP   string
	pending    []signaling.WebP2PCandidate
	remoteCh   chan struct{}
	dc         *datachannel.DataChannel
	closers    []func() error
	closed     bool

	open   chan struct{}
	done   chan struct{}
	err    error
	ctx    context.Context
	cancel context.CancelFunc
}

// Allowed reports whether a call may try the direct path.
func Allowed(isGroup, protectIP, callerIsContact, isCaller bool, settings VoipSettings) bool {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L42
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L80-L83
	switch {
	case isGroup, protectIP, settings.DisableP2P, !settings.EnableWebP2P:
		return false
	case !isCaller && !callerIsContact:
		return false
	}
	return true
}

// Dial starts ICE with the peer and returns once local credentials exist; the
// path opens later (Opened), or the attempt fails (Done) and the call stays on
// the relay.
func Dial(ctx context.Context, cfg Config, opts ...Option) (*Conn, error) {
	// NOT VALIDATED: loopback test only; validated once a live call opens the path (the server sends disable_p2p=1 today).
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L36-L42
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L59-L64
	rc := resolveConfig(opts)
	lg := rc.log.With().Str("call_id", cfg.CallID).Bool("is_caller", cfg.IsCaller).Bool("peer_is_web", cfg.PeerIsWeb).Logger()
	if cfg.SendTransport == nil {
		return nil, errors.New("p2p: Config.SendTransport is required")
	}
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return nil, fmt.Errorf("p2p: self-signed cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("p2p: parse cert: %w", err)
	}
	fp, err := fingerprint.Fingerprint(leaf, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("p2p: fingerprint: %w", err)
	}

	agentOpts := []ice.AgentOption{
		ice.WithUrls(stunURIs(cfg)),
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
		ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost, ice.CandidateTypeServerReflexive}),
		ice.WithMulticastDNSMode(ice.MulticastDNSModeDisabled),
	}
	if cfg.IncludeLoopback {
		agentOpts = append(agentOpts, ice.WithIncludeLoopback())
	}
	agent, err := ice.NewAgentWithOptions(agentOpts...)
	if err != nil {
		return nil, fmt.Errorf("p2p: ice agent: %w", err)
	}
	ufrag, pwd, err := agent.GetLocalUserCredentials()
	if err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("p2p: ice credentials: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	c := &Conn{
		cfg: cfg, log: lg, cert: cert, agent: agent,
		ufrag: ufrag, pwd: pwd, fp: strings.ToUpper(fp),
		remoteCh: make(chan struct{}), open: make(chan struct{}), done: make(chan struct{}),
		ctx: cctx, cancel: cancel,
	}
	c.closers = append(c.closers, agent.Close)

	if err := agent.OnCandidate(c.onLocalCandidate); err != nil {
		c.fail(err)
		return nil, fmt.Errorf("p2p: candidate handler: %w", err)
	}
	if err := cfg.SendTransport(c.LocalTransport()); err != nil {
		lg.Warn().Err(err).Msg("p2p: sending initial transport failed")
	}
	if err := agent.GatherCandidates(); err != nil {
		c.fail(err)
		return nil, fmt.Errorf("p2p: gather: %w", err)
	}
	lg.Debug().Int("stun_servers", len(cfg.STUNServers)).Msg("p2p: started")
	go c.connect()
	return c, nil
}

func stunURIs(cfg Config) []*stun.URI {
	var uris []*stun.URI
	add := func(host string) {
		if u, err := stun.ParseURI("stun:" + host); err == nil {
			uris = append(uris, u)
		}
	}
	for _, s := range cfg.STUNServers {
		if len(uris) >= maxSTUNServers {
			break
		}
		add(s.String())
	}
	if !cfg.SkipFallbackSTUN {
		add(FallbackSTUNServer)
	}
	return uris
}

func (c *Conn) onLocalCandidate(cand ice.Candidate) {
	if cand == nil {
		c.log.Debug().Msg("p2p: ice gathering complete")
		return
	}
	if cand.NetworkType().IsTCP() {
		return
	}
	ip, err := netip.ParseAddr(cand.Address())
	if err != nil {
		return
	}
	wc := signaling.WebP2PCandidate{IP: ip, Port: uint16(cand.Port()), Priority: wirePriority(cand.Priority())}
	c.mu.Lock()
	c.candidates = append(c.candidates, wc)
	c.mu.Unlock()
	t := c.LocalTransport()
	t.Candidates = []signaling.WebP2PCandidate{wc}
	if err := c.cfg.SendTransport(t); err != nil {
		c.log.Warn().Err(err).Msg("p2p: sending candidate failed")
	}
}

func wirePriority(p uint32) uint8 {
	v := p >> candidatePriorityShift
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

func (c *Conn) connect() {
	select {
	case <-c.remoteCh:
	case <-c.ctx.Done():
		c.fail(fmt.Errorf("p2p: no remote credentials: %w", c.ctx.Err()))
		return
	}
	c.mu.Lock()
	rUfrag, rPwd := c.remoteUfrg, c.remotePwd
	c.mu.Unlock()

	var iceConn *ice.Conn
	var err error
	if c.cfg.IsCaller {
		iceConn, err = c.agent.Dial(c.ctx, rUfrag, rPwd)
	} else {
		iceConn, err = c.agent.Accept(c.ctx, rUfrag, rPwd)
	}
	if err != nil {
		c.fail(fmt.Errorf("p2p: ice: %w", err))
		return
	}
	c.log.Debug().Msg("p2p: ice connected")

	pc := dtlsnet.PacketConnFromConn(iceConn)
	dtlsOpts := []dtls.Option{
		dtls.WithCertificates(c.cert),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithVerifyPeerCertificate(c.verifyPeer),
	}
	client := c.dtlsClient()
	var dconn *dtls.Conn
	if client {
		dconn, err = dtls.ClientWithOptions(pc, iceConn.RemoteAddr(), toClientOpts(dtlsOpts)...)
	} else {
		dconn, err = dtls.ServerWithOptions(pc, iceConn.RemoteAddr(), toServerOpts(dtlsOpts)...)
	}
	if err == nil {
		err = dconn.HandshakeContext(c.ctx)
	}
	if err != nil {
		c.fail(fmt.Errorf("p2p: dtls: %w", err))
		return
	}
	c.addCloser(dconn.Close)
	c.log.Debug().Bool("dtls_client", client).Msg("p2p: dtls connected")

	var assoc *sctp.Association
	if client {
		assoc, err = sctp.ClientWithOptions(sctp.WithNetConn(dconn), sctp.WithName(DataChannelLabel))
	} else {
		assoc, err = sctp.ServerWithOptions(sctp.WithNetConn(dconn), sctp.WithName(DataChannelLabel))
	}
	if err != nil {
		c.fail(fmt.Errorf("p2p: sctp: %w", err))
		return
	}
	c.addCloser(assoc.Close)

	dc, err := datachannel.Dial(assoc, DataChannelID, &datachannel.Config{
		Negotiated:           true,
		Label:                DataChannelLabel,
		ChannelType:          datachannel.ChannelTypePartialReliableRexmitUnordered,
		ReliabilityParameter: 0,
		LoggerFactory:        logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		c.fail(fmt.Errorf("p2p: datachannel: %w", err))
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = dc.Close()
		return
	}
	c.dc = dc
	c.closers = append(c.closers, dc.Close)
	c.mu.Unlock()
	c.log.Info().Msg("p2p: data channel open")
	close(c.open)
}

// dtlsClient follows WA Web's roles: the caller is the DTLS client toward a
// web peer; toward a native peer we are always the server.
func (c *Conn) dtlsClient() bool {
	return c.cfg.PeerIsWeb && c.cfg.IsCaller
}

func (c *Conn) verifyPeer(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return errFingerprint
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return err
	}
	got, err := fingerprint.Fingerprint(cert, crypto.SHA256)
	if err != nil {
		return err
	}
	c.mu.Lock()
	want := c.remoteFP
	c.mu.Unlock()
	if !strings.EqualFold(got, want) {
		return errFingerprint
	}
	return nil
}

func toClientOpts(in []dtls.Option) []dtls.ClientOption {
	out := make([]dtls.ClientOption, len(in))
	for i, o := range in {
		out[i] = o
	}
	return out
}

func toServerOpts(in []dtls.Option) []dtls.ServerOption {
	out := make([]dtls.ServerOption, len(in))
	for i, o := range in {
		out[i] = o
	}
	return out
}

func (c *Conn) addCloser(f func() error) {
	c.mu.Lock()
	c.closers = append(c.closers, f)
	c.mu.Unlock()
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.log.Debug().Err(err).Msg("p2p: attempt failed, staying on relay")
	_ = c.Close()
}

// Opened is closed once the data channel is open.
func (c *Conn) Opened() <-chan struct{} { return c.open }

// Done is closed when the connection ends or the attempt fails; Err says why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is the reason the connection ended, if any.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// LocalTransport is our current credentials and candidates as a type 13 transport.
func (c *Conn) LocalTransport() *signaling.WebP2PTransport {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L36-L40
	c.mu.Lock()
	defer c.mu.Unlock()
	return &signaling.WebP2PTransport{
		CallID: c.cfg.CallID, Round: "1",
		ICEUfrag: c.ufrag, ICEPwd: c.pwd,
		CertAlgorithm: certFingerprintAlgo, CertFingerprint: c.fp,
		Candidates: append([]signaling.WebP2PCandidate(nil), c.candidates...),
	}
}

// AddRemote applies the peer's type 13 transport. It returns ErrPeerRestart
// when the peer's ufrag changed.
func (c *Conn) AddRemote(t *signaling.WebP2PTransport) error {
	// NOT VALIDATED: loopback test only.
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L39-L42
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errClosed
	}
	if t.ICEUfrag != "" {
		if c.remoteSet && t.ICEUfrag != c.remoteUfrg {
			c.mu.Unlock()
			c.log.Debug().Msg("p2p: peer ice restart")
			return ErrPeerRestart
		}
		if !c.remoteSet {
			if t.ICEPwd == "" || t.CertFingerprint == "" {
				c.mu.Unlock()
				return errNoRemote
			}
			c.remoteSet = true
			c.remoteUfrg, c.remotePwd, c.remoteFP = t.ICEUfrag, t.ICEPwd, t.CertFingerprint
			close(c.remoteCh)
		}
	}
	var add []signaling.WebP2PCandidate
	if c.remoteSet {
		add = append(c.pending, t.Candidates...)
		c.pending = nil
	} else {
		for _, cand := range t.Candidates {
			if len(c.pending) < maxBufferedCandidates {
				c.pending = append(c.pending, cand)
			} else {
				c.log.Warn().Msg("p2p: remote candidate buffer full, dropping")
			}
		}
	}
	c.mu.Unlock()
	for _, cand := range add {
		ic, err := ice.UnmarshalCandidate(remoteCandidateLine(cand))
		if err != nil {
			c.log.Warn().Err(err).Msg("p2p: bad remote candidate")
			continue
		}
		if err := c.agent.AddRemoteCandidate(ic); err != nil {
			c.log.Warn().Err(err).Msg("p2p: adding remote candidate failed")
		}
	}
	c.log.Debug().Int("candidates", len(add)).Msg("p2p: applied remote transport")
	return nil
}

func remoteCandidateLine(c signaling.WebP2PCandidate) string {
	prio := uint32(c.Priority) << candidatePriorityShift
	return "candidate:1 1 UDP " + strconv.FormatUint(uint64(prio), 10) + " " + c.IP.String() + " " + strconv.Itoa(int(c.Port)) + " typ host"
}

// Send writes one packet on the open data channel.
func (c *Conn) Send(pkt []byte) error {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L52-L57
	c.mu.Lock()
	dc := c.dc
	c.mu.Unlock()
	if dc == nil {
		return errNotOpen
	}
	_, err := dc.WriteDataChannel(pkt, false)
	return err
}

// Recv returns the next packet from the data channel.
func (c *Conn) Recv(ctx context.Context) ([]byte, error) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L52-L57
	select {
	case <-c.open:
	case <-c.done:
		return nil, errClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	dc := c.dc
	c.mu.Unlock()
	if dc == nil {
		return nil, errClosed
	}
	buf := make([]byte, 1500)
	n, _, err := dc.ReadDataChannel(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Close ends the P2P attempt. It is safe to call more than once.
func (c *Conn) Close() error {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L59-L64
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	closers := c.closers
	c.closers = nil
	c.mu.Unlock()
	c.cancel()
	for i := len(closers) - 1; i >= 0; i-- {
		_ = closers[i]()
	}
	close(c.done)
	return nil
}
