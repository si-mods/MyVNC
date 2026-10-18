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
	Logger   *slog.Logger
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
	Lost         uint64 // losses reported by the depacketizer
	// Late counts sequence gaps a late packet filled; Missing, picture
	// packets given up as lost.
	Late, Missing uint64
	RR, RCTL      uint64 // feedback sent
	PLI, Refresh  uint64
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

	mu    sync.Mutex
	stats Stats
	w, h  uint16 // the answered picture size, for Refresh
}

// Listen binds the audio and video ports on cfg.Local, connects them to
// the same ports on cfg.Peer, and starts receiving. Bind it before the
// offer goes out: the Mac sends as soon as it has answered.
func Listen(cfg StreamConfig) (*Stream, error) {
	s := &Stream{cfg: cfg, log: cfg.Logger, done: make(chan struct{}), keyframe: make(chan struct{}, 1)}
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
func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// SetVideoSize sets the picture size a refresh request names, from the
// answer; without it a loss asks for a keyframe.
func (s *Stream) SetVideoSize(w, h int) {
	s.mu.Lock()
	s.w, s.h = uint16(w), uint16(h)
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
		order      reorder
		feedback   = NewRateFeedback(time.Now())
		mediaSSRC  uint32
		owed       ask
		asked      time.Time
		unacked    *uint32
		report     = time.NewTicker(reportInterval)
		rate       = time.NewTicker(rateInterval)
		firstVideo = true
		firstAudio = true
	)
	defer report.Stop()
	defer rate.Stop()
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
			if r := feedback.Report(sec.VideoSSRC, now); r != nil {
				send(s.video, videoOut.Protect(r), &s.stats.RCTL)
			}
			for _, p := range order.Expire() {
				s.depacketize(&dep, p, &owed, &unacked)
			}
			s.mu.Lock()
			s.stats.Missing = order.Missing
			s.mu.Unlock()
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
			}
			for _, p := range order.push(h, d[h.Payload[0]:h.Payload[1]]) {
				s.depacketize(&dep, p, &owed, &unacked)
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

// depacketize hands one in-order picture packet to dep, a whole unit on
// to OnUnit, and notes what to acknowledge or ask for.
func (s *Stream) depacketize(dep *Depacketizer, p orderedPacket, owed *ask, unacked **uint32) {
	u, lost := dep.Push(p.h, p.payload)
	switch {
	case u != nil:
		*owed = askNone
		s.mu.Lock()
		s.stats.Units++
		if u.Key {
			s.stats.Keys++
		}
		s.mu.Unlock()
		if s.cfg.OnUnit != nil {
			s.cfg.OnUnit(u)
		}
		ts := u.Timestamp
		*unacked = &ts
	case lost:
		s.mu.Lock()
		s.stats.Lost++
		s.mu.Unlock()
		a := askKeyframe
		if dep.ResumesAtRefresh() {
			a = askRefresh
		}
		*owed = max(*owed, a)
	}
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
