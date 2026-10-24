// The UDP legs: SRTP in, access units out, RTCP feedback back.
//
// Portions derived from remotex, Copyright 2026 Andrew Chen, Alessandro
// Barbosa, MIT License (src/vnc_apple_media.rs, Receiver).
// SPDX-License-Identifier: GPL-2.0-or-later

package media

import (
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// videoReceiveBuffer: the Mac sends a picture as one burst at link
	// speed; remotex lost keyframe fragments with Linux's default 208 KB.
	videoReceiveBuffer = 4 << 20
	rateInterval       = 50 * time.Millisecond
	reportInterval     = time.Second
	// keyframeInterval is the least time between keyframe requests; the Mac
	// drops one made within a second of its last keyframe, so a request
	// stays owed and is made again.
	keyframeInterval = 500 * time.Millisecond
	// nackRetries is the most times a sequence number is asked for; the
	// gaps reorder gave up are covered by Refresh if that is not enough.
	nackRetries = 3
	// nackMinGap is the least time between asks for one sequence number.
	nackMinGap = 200 * time.Millisecond
	// nackGiveUp: after this a sequence is too old to retransmit; the
	// keyframe path takes over.
	nackGiveUp = 2 * time.Second
)

// StreamConfig configures Listen.
type StreamConfig struct {
	// Local is this side's address on the RFB connection, Peer the Mac's.
	Local, Peer          net.IP
	AudioPort, VideoPort int // as the Mac named them; bound and connected on both ends
	Secrets              *Secrets
	DONL                 bool // see Depacketizer.DONL
	// OnUnit receives each decodable access unit on the receiver's
	// goroutine; the unit is acknowledged to the Mac when OnUnit returns.
	// Hand it to a decoder queue rather than decoding here.
	OnUnit func(*Unit)
	// OnPacket, when set, sees each picture-leg RTP datagram on the
	// receiver's goroutine, for debugging: decrypted, without its tag,
	// when err is nil; as received with ErrStale or ErrForged. It must
	// not keep packet.
	OnPacket func(at time.Time, h RTPHeader, packet []byte, err error)
	// OnAudio, when set, receives each audio access unit off the audio
	// leg (payload 101) on the receiver's goroutine; nil drops audio, as
	// it is by default. Hand it to a queue rather than decoding here.
	OnAudio func(*AudioUnit)
	// OnClock, when set, receives the picture-leg presentation clock
	// before receiving starts; SRs from the RTCP leg keep it anchored.
	OnClock func(*PresentationClock)
	// MaxKbps caps the adaptive RCTL bandwidth estimate; 0 is the Mac's
	// 60 Mbit/s ceiling.
	MaxKbps int
	// GOP, when positive, asks for a keyframe after that many pictures,
	// bounding the effective group of pictures.
	GOP    int
	Logger *slog.Logger
}

// LegStats count one UDP leg.
type LegStats struct {
	Datagrams, Bytes   uint64
	SRTPOK, SRTPForged uint64
	SRTPStale, NotRTP  uint64
	RTCPOK, RTCPForged uint64
	PayloadType        uint8  // of the last authentic RTP packet
	SSRC               uint32 // the Mac's, of the last authentic RTP packet
	ReadErrors         uint64
}

// Stats are a Stream's counters since Listen.
type Stats struct {
	Audio, Video LegStats
	Units, Keys  uint64 // access units handed on, and key pictures among them
	KeyBytes     uint64 // wire bytes in key pictures
	SinceKey     int    // pictures since the last key picture
	AudioUnits   uint64 // audio access units handed to OnAudio
	Recoveries   uint64 // loss episodes ended by a key picture
	RecoveryMs   int64  // the last recovery, in milliseconds
	Lost         uint64 // losses reported by the depacketizer
	// Late counts sequence gaps a late packet filled; Missing, picture
	// packets given up as lost.
	Late, Missing uint64
	RR, RCTL      uint64 // feedback sent
	PLI, Refresh  uint64
	NACK          uint64 // retransmission requests sent
	FrameLossSent uint64 // Apple type-6 damaged-frame reports
	FIRSent       uint64 // full intra requests after unanswered PLIs
	ClockSyncs    uint64 // RTCP sender reports absorbed by the clock
	TargetKbps    int    // adaptive RCTL bandwidth estimate
	Acks          uint64
	SendErrors    uint64
	LastSendError string
}

// Stream receives one High Performance session's media.
type Stream struct {
	cfg          StreamConfig
	audio, video *net.UDPConn
	log          *slog.Logger
	done         chan struct{}
	closeOnce    sync.Once
	wg           sync.WaitGroup
	keyframe     chan struct{}
	decodeCap    atomic.Int64 // kbit/s, 0 for none

	mu       sync.Mutex
	stats    Stats
	w, h     uint16 // the answered picture size, for Refresh
	sinceKey int    // pictures since the last key picture, for GOP
	lossAt   time.Time

	// frame tracking from Apple's media-control extension: the frame in
	// flight and the last one that ended damaged, reported once.
	framePackets, frameSeq uint16
	frameTS                uint32
	frameLost              uint16 // packets of the frame in flight that never came
	frameSeen              bool
	damage                 *frameDamage // nil when none pending
	firSeq                 uint8
	plisSinceFIR           int
	// bands is the compound depacketizer when the Mac spreads pictures
	// over several SSRCs; nil while one band is enough. bandN is the
	// answer's Tiles; loop reads both between packets.
	bands  *compound
	bandN  int
	clock  *PresentationClock // RTP-to-wall mapping from SRs
}

// frameDamage is a frame that ended with packets missing.
type frameDamage struct {
	ts    uint32
	seq   uint16
	total uint16
	lost  uint16
}

// Listen binds the audio and video ports on cfg.Local, connects them to
// the same ports on cfg.Peer, and starts receiving. Bind it before the
// offer goes out: the Mac sends as soon as it has answered.
func Listen(cfg StreamConfig) (*Stream, error) {
	s := &Stream{cfg: cfg, log: cfg.Logger, done: make(chan struct{}), keyframe: make(chan struct{}, 1), clock: NewPresentationClock(videoClockRate)}
	if cfg.OnClock != nil {
		cfg.OnClock(s.clock)
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	var err error
	if s.audio, err = dialLeg(cfg.Local, cfg.Peer, cfg.AudioPort, 0); err != nil {
		return nil, err
	}
	if s.video, err = dialLeg(cfg.Local, cfg.Peer, cfg.VideoPort, videoReceiveBuffer); err != nil {
		s.audio.Close()
		return nil, err
	}
	s.log.Info("media: listening", "audio", s.audio.LocalAddr(), "video", s.video.LocalAddr(), "peer", cfg.Peer)
	// An empty datagram on each leg before the offer: a NAT between us
	// and the Mac maps our ports now, before the Mac's first packets
	// arrive and take the mapping (seen through a WireGuard NAT, where our
	// later RTCP then left from another port and no media got through).
	s.audio.Write(nil)
	s.video.Write(nil)
	audioIn, videoIn := make(chan []byte, 256), make(chan []byte, 4096)
	s.wg.Add(3)
	go s.read(s.audio, audioIn, &s.stats.Audio)
	go s.read(s.video, videoIn, &s.stats.Video)
	go s.loop(audioIn, videoIn)
	return s, nil
}

func dialLeg(local, peer net.IP, port, rcvbuf int) (*net.UDPConn, error) {
	d := net.Dialer{LocalAddr: &net.UDPAddr{IP: local, Port: port}, Control: control}
	c, err := d.Dial("udp", net.JoinHostPort(peer.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	uc := c.(*net.UDPConn)
	if rcvbuf > 0 {
		uc.SetReadBuffer(rcvbuf)
	}
	return uc, nil
}

// Close stops the stream and closes its sockets.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.audio.Close()
		s.video.Close()
	})
	s.wg.Wait()
	return nil
}

// Stats returns a copy of the counters.
func unitBytes(u *Unit) uint64 {
	var n uint64
	for _, p := range u.NALs {
		n += uint64(len(p))
	}
	return n
}

func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// PresentationClock returns the clock fed by the legs' sender reports,
// for computing a frame's absolute presentation time. Any goroutine.
func (s *Stream) PresentationClock() *PresentationClock { return s.clock }

// SetVideoSize sets the picture size a refresh request names, from the
// answer; without it a loss asks for a keyframe.
func (s *Stream) SetVideoSize(w, h int) {
	s.mu.Lock()
	s.w, s.h = uint16(w), uint16(h)
	s.mu.Unlock()
}

// SetDecodeCap limits the RCTL bandwidth estimate to kbps, the most this
// side's decoder can take; 0 lifts it. Any goroutine.
func (s *Stream) SetDecodeCap(kbps int) { s.decodeCap.Store(int64(max(kbps, 0))) }

// SetBandCount sets how many SSRCs one picture is spread over, from the
// answer's Tiles; 1 (or 0) keeps the single-band depacketizer. From then
// on the compound merges the bands' units per picture. Any goroutine.
func (s *Stream) SetBandCount(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	s.bandN = n
	if n > 1 {
		if s.bands == nil {
			s.bands = newCompound(n)
		} else {
			s.bands.SetBandCount(n)
		}
	}
	s.mu.Unlock()
}

// RequestKeyframe asks the Mac for an IDR, as after a decoder failure.
func (s *Stream) RequestKeyframe() {
	select {
	case s.keyframe <- struct{}{}:
	default:
	}
}

func (s *Stream) read(c *net.UDPConn, out chan<- []byte, st *LegStats) {
	defer s.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, err := c.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// ICMP unreachable for a report sent before the Mac was up,
			// and the like; keep reading.
			s.mu.Lock()
			st.ReadErrors++
			s.mu.Unlock()
			select {
			case <-s.done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		s.mu.Lock()
		st.Datagrams++
		st.Bytes += uint64(n)
		s.mu.Unlock()
		select {
		case out <- append([]byte(nil), buf[:n]...):
		case <-s.done:
			return
		}
	}
}

type ask int

const (
	askNone ask = iota
	askRefresh
	askKeyframe
)

// nackTracker tracks per-sequence retransmission asks.
type nackTracker struct {
	first, last time.Time // first loss, last ask
	retries     int
}

// nackState holds sequences awaiting retransmission. Callers hold s.mu.
type nackState struct {
	pending map[uint16]*nackTracker
}

func (n *nackState) add(now time.Time, seqs []uint16) {
	if len(seqs) == 0 {
		return
	}
	if n.pending == nil {
		n.pending = map[uint16]*nackTracker{}
	}
	for _, s := range seqs {
		if _, ok := n.pending[s]; !ok {
			n.pending[s] = &nackTracker{first: now, last: now}
		}
	}
}

// send builds one NACK for the sequences due now and drops those given
// up on; it returns the packet's sequence list, nil when none is due.
func (n *nackState) send(now time.Time) []uint16 {
	if len(n.pending) == 0 {
		return nil
	}
	var seqs []uint16
	for s, t := range n.pending {
		if now.Sub(t.first) > nackGiveUp {
			delete(n.pending, s)
		} else if t.retries < nackRetries && now.Sub(t.last) >= nackMinGap {
			seqs = append(seqs, s)
			t.retries++
			t.last = now
		}
	}
	return seqs
}

// arrived stops asking for a sequence a NACK recovered.
func (n *nackState) arrived(seq uint16) { delete(n.pending, seq) }

// seqRange lists first..last-1.
func seqRange(first, last uint16) []uint16 {
	if first == last {
		return nil
	}
	out := make([]uint16, 0, int(last-first))
	for s := first; s != last; s++ {
		out = append(out, s)
	}
	return out
}

func (s *Stream) loop(audioIn, videoIn <-chan []byte) {
	defer s.wg.Done()
	sec := s.cfg.Secrets
	var (
		audioSRTP  = NewSRTPReceiver(&sec.Audio.Down)
		audioRTCP  = NewSRTCPReceiver(&sec.Audio.Down)
		audioOut   = NewSRTCPSender(&sec.Audio.Up)
		videoSRTP  = NewSRTPReceiver(&sec.Video.Down)
		videoRTCP  = NewSRTCPReceiver(&sec.Video.Down)
		videoOut   = NewSRTCPSender(&sec.Video.Up)
		dep        = Depacketizer{DONL: s.cfg.DONL}
		audioDep   AudioDepacketizer
		audioSSRC  uint32
		order      reorder
		feedback   = NewRateFeedback(time.Now())
		rateCtl    = newRateControl(s.cfg.MaxKbps)
		mediaSSRC  uint32
		owed       ask
		asked      time.Time
		unacked    *uint32
		report     = time.NewTicker(reportInterval)
		rate       = time.NewTicker(rateInterval)
		firstVideo = true
		firstAudio = true
		nacks      nackState
		lastSeq    uint16
		haveSeq    bool
		lastPLIAt  time.Time
	)
	defer report.Stop()
	defer rate.Stop()
	s.mu.Lock()
	s.stats.TargetKbps = rateCtl.target
	s.mu.Unlock()
	send := func(c *net.UDPConn, b []byte, counter *uint64) {
		_, err := c.Write(b)
		s.mu.Lock()
		*counter++
		if err != nil {
			s.stats.SendErrors++
			s.stats.LastSendError = err.Error()
		}
		s.mu.Unlock()
	}
	for {
		select {
		case <-s.done:
			return
		case <-report.C:
			send(s.audio, audioOut.Protect(ReceiverReport(sec.AudioSSRC)), &s.stats.RR)
			send(s.video, videoOut.Protect(ReceiverReport(sec.VideoSSRC)), &s.stats.RR)
		case now := <-rate.C:
			s.mu.Lock()
			bytes, missing := s.stats.Video.Bytes, s.stats.Missing
			s.mu.Unlock()
			rateCtl.setDecodeCap(int(s.decodeCap.Load()))
			target := rateCtl.step(now, bytes, missing, feedback.Delay())
			feedback.SetBandwidth(target)
			s.mu.Lock()
			s.stats.TargetKbps = target
			s.mu.Unlock()
			if r := feedback.Report(sec.VideoSSRC, now); r != nil {
				send(s.video, videoOut.Protect(r), &s.stats.RCTL)
			}
			for _, p := range order.Expire() {
				s.packetize(&dep, p, &owed, &unacked)
			}
			s.mu.Lock()
			s.stats.Missing = order.Missing
			nacks.add(now, order.Gaps())
			due := nacks.send(now)
			s.mu.Unlock()
			if len(due) > 0 {
				send(s.video, videoOut.Protect(NACK(sec.VideoSSRC, mediaSSRC, due)), &s.stats.NACK)
			}
		case <-s.keyframe:
			dep.Resync()
			owed = askKeyframe
		case d := <-audioIn:
			h, err := audioSRTP.Unprotect(d)
			s.mu.Lock()
			s.count(&s.stats.Audio, h, err, audioRTCP, d)
			s.mu.Unlock()
			if err == nil && firstAudio {
				firstAudio = false
				s.log.Info("media: sound is flowing", "ssrc", h.SSRC, "pt", h.PayloadType)
			}
			if err != nil || s.cfg.OnAudio == nil || h.PayloadType != AudioPayloadType {
				break
			}
			if h.SSRC != audioSSRC { // a new audio stream starts over
				audioSSRC = h.SSRC
				audioDep = AudioDepacketizer{}
			}
			u, err := audioDep.Push(h, d[h.Payload[0]:h.Payload[1]])
			if err != nil {
				s.log.Debug("media: bad audio packet", "seq", h.Sequence, "err", err)
				break
			}
			if u == nil {
				break
			}
			s.mu.Lock()
			s.stats.AudioUnits++
			s.mu.Unlock()
			s.cfg.OnAudio(u)
		case d := <-videoIn:
			h, err := videoSRTP.Unprotect(d)
			s.mu.Lock()
			s.count(&s.stats.Video, h, err, videoRTCP, d)
			s.mu.Unlock()
			if s.cfg.OnPacket != nil && (err == nil || err == ErrStale || err == ErrForged) {
				p := d
				if err == nil {
					p = d[:h.Payload[1]]
				}
				s.cfg.OnPacket(time.Now(), h, p, err)
			}
			if err != nil {
				break
			}
			if firstVideo {
				firstVideo = false
				s.log.Info("media: picture is flowing", "ssrc", h.SSRC, "pt", h.PayloadType)
			}
			feedback.Received(h.SSRC, h.Timestamp, time.Now())
			if h.SSRC != mediaSSRC {
				mediaSSRC, owed = h.SSRC, askNone // a new stream starts with its own IDR
				haveSeq = false
				nacks = nackState{}
			}
			// A packet that arrives right after a NACK stops the asks.
			s.mu.Lock()
			nacks.arrived(h.Sequence)
			s.mu.Unlock()
			if haveSeq && h.Sequence != lastSeq+1 {
				// A gap appeared at push time; ask at the next rate tick
				// if reorder does not fill it first.
				gap := int16(h.Sequence-lastSeq) - 1
				if gap > 0 {
					s.mu.Lock()
					nacks.add(time.Now(), seqRange(lastSeq+1, h.Sequence))
					s.mu.Unlock()
				}
			}
			lastSeq, haveSeq = h.Sequence, true
			s.trackFrame(h)
			for _, p := range order.push(h, d[h.Payload[0]:h.Payload[1]]) {
				s.packetize(&dep, p, &owed, &unacked)
			}
			s.mu.Lock()
			s.stats.Late, s.stats.Missing = order.Late, order.Missing
			s.mu.Unlock()
		}
		if unacked != nil {
			send(s.video, videoOut.Protect(PictureAck(sec.VideoSSRC, *unacked)), &s.stats.Acks)
			unacked = nil
		}
		if owed != askNone && mediaSSRC != 0 && time.Since(asked) >= keyframeInterval {
			asked = time.Now()
			if d := s.takeDamage(); d != nil && d.lost <= 255 && d.total <= 255 {
				send(s.video, videoOut.Protect(FrameLoss(sec.VideoSSRC, mediaSSRC, FrameLossInfo{
					FrameRTPTimestamp: d.ts,
					FrameSeq:          d.seq,
					PacketCount:       uint8(d.total),
					LostCount:         uint8(d.lost),
				})), &s.stats.FrameLossSent)
			}
			s.mu.Lock()
			s.plisSinceFIR++
			s.mu.Unlock()
			if s.plisSinceFIR >= 2 && time.Since(lastPLIAt) >= 2*time.Second {
				s.mu.Lock()
				s.plisSinceFIR = 0
				seq := s.firSeq
				s.firSeq++
				s.mu.Unlock()
				send(s.video, videoOut.Protect(FIR(sec.VideoSSRC, mediaSSRC, seq)), &s.stats.FIRSent)
			} else {
				lastPLIAt = time.Now()
			}
			s.mu.Lock()
			w, h := s.w, s.h
			s.mu.Unlock()
			if owed == askRefresh && w > 0 && h > 0 {
				send(s.video, videoOut.Protect(Refresh(sec.VideoSSRC, mediaSSRC, w, h)), &s.stats.Refresh)
			} else {
				send(s.video, videoOut.Protect(PLI(sec.VideoSSRC, mediaSSRC)), &s.stats.PLI)
			}
		}
	}
}

// packetize hands one in-order picture packet to the compound or the
// single-band depacketizer, a whole unit on to OnUnit, and notes what to
// acknowledge or ask for. The caller reads s.bands and s.bandN before it,
// between packets.
func (s *Stream) packetize(dep *Depacketizer, p orderedPacket, owed *ask, unacked **uint32) {
	s.mu.Lock()
	c, dn := s.bands, s.bandN
	s.mu.Unlock()
	if c == nil && dn > 1 {
		c = newCompound(dn)
		s.mu.Lock()
		s.bands = c
		s.mu.Unlock()
	}
	if c != nil {
		c.SetBandCount(max(dn, 1))
	}
	u, lost := c.push(p.h, p.payload)
	switch {
	case u != nil:
		*owed = askNone
		s.mu.Lock()
		s.plisSinceFIR = 0
		s.stats.Units++
		if u.Key {
			s.stats.Keys++
			s.stats.KeyBytes += unitBytes(u)
			if !s.lossAt.IsZero() {
				s.stats.Recoveries++
				s.stats.RecoveryMs = time.Since(s.lossAt).Milliseconds()
				s.lossAt = time.Time{}
			}
			s.sinceKey = 0
		} else {
			s.sinceKey++
			if s.cfg.GOP > 0 && s.sinceKey >= s.cfg.GOP {
				s.sinceKey = 0
				s.RequestKeyframe()
			}
		}
		s.stats.SinceKey = s.sinceKey
		s.mu.Unlock()
		if s.cfg.OnUnit != nil {
			s.cfg.OnUnit(u)
		}
		ts := u.Timestamp
		*unacked = &ts
	case lost:
		s.mu.Lock()
		s.stats.Lost++
		if s.lossAt.IsZero() {
			s.lossAt = time.Now()
		}
		s.mu.Unlock()
		s.frameMisses(1)
		a := askKeyframe
		if c != nil {
			// A lost band picture is out of step by lost packets alone;
			// the Mac's refresh picture (on the band that lost it)
			// resumes the stream.
			a = askRefresh
		} else if dep.ResumesAtRefresh() {
			a = askRefresh
		}
		*owed = max(*owed, a)
	}
}

// trackFrame follows the frame in flight through Apple's media-control
// extension: a packet carrying a frame sequence number closes the previous
// frame, a damaged one is recorded once for a type-6 frame-loss report.
func (s *Stream) trackFrame(h RTPHeader) {
	if !h.HasFrameExt {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frameSeen && s.damage == nil && s.frameLost > 0 {
		s.damage = &frameDamage{ts: s.frameTS, seq: s.frameSeq, total: s.framePackets, lost: s.frameLost}
	}
	s.framePackets, s.frameSeq, s.frameTS, s.frameSeen = h.FramePackets, h.FrameSeq, h.Timestamp, true
	s.frameLost = 0
}

// frameMisses notes packets of the frame in flight that did not arrive.
func (s *Stream) frameMisses(n int) {
	s.mu.Lock()
	s.frameLost += uint16(n)
	s.mu.Unlock()
}

// takeDamage returns the pending damaged frame, if any, once per frame.
func (s *Stream) takeDamage() *frameDamage {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.damage
	s.damage = nil
	return d
}

// count records one datagram's fate; s.mu is held.
func (s *Stream) count(st *LegStats, h RTPHeader, err error, rtcp *SRTCPReceiver, d []byte) {
	switch err {
	case nil:
		st.SRTPOK++
		st.PayloadType, st.SSRC = h.PayloadType, h.SSRC
	case ErrRTCP:
		if rtcp.Authenticate(d) == nil {
			st.RTCPOK++
			if ssrc, ntpSec, ntpFrac, rtpTS, ok := ParseSenderReport(d); ok {
				s.clock.Note(ssrc, ntpSec, ntpFrac, rtpTS)
				s.mu.Lock()
				s.stats.ClockSyncs++
				s.mu.Unlock()
			}
		} else {
			st.RTCPForged++
		}
	case ErrForged:
		st.SRTPForged++
	case ErrStale:
		st.SRTPStale++
	default:
		st.NotRTP++
	}
}
