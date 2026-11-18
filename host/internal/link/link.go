// Package link runs one protocol session over the CONTROL port: sequence
// numbers, the send window, acknowledgements and retransmission, the
// heartbeat, board events, and the host's own model of what it is holding
// down. The simulator and board implement the same serial protocol.
package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/serial"
)

// Port is what a Session drives. ReadTimeout returns serial.ErrTimeout when
// nothing arrived within the duration and io.EOF once the port is closed.
type Port interface {
	ReadTimeout(b []byte, d time.Duration) (int, error)
	Write(b []byte) (int, error)
	Close() error
}

// ModemStatus is implemented by ports that can read CTS.
type ModemStatus interface {
	CTS() (bool, error)
}

// Options tune a session. Zero values take the protocol defaults.
type Options struct {
	// AckTimeout is how long a frame may go unacknowledged before it is
	// sent again. Protocol default 1 s.
	AckTimeout time.Duration
	// Retries is how many times a frame is sent again before the link is
	// declared lost. Protocol default 5.
	Retries int
	// Heartbeat is the HEARTBEAT interval. Protocol default 100 ms. A
	// negative value disables the heartbeat; tests use it to provoke the
	// board's watchdog.
	Heartbeat time.Duration
	// Logger receives link diagnostics at debug level and losses at warn.
	Logger *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.AckTimeout <= 0 {
		o.AckTimeout = time.Second
	}
	if o.Retries <= 0 {
		o.Retries = 5
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = 100 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return o
}

var (
	// ErrLinkLost is a frame that stayed unacknowledged through every retry.
	ErrLinkLost = errors.New("link lost: no acknowledgement after retries")
	// ErrClosed is a send on a closed session.
	ErrClosed = errors.New("session closed")
	// ErrDesync is a NAK SEQUENCE naming a frame this session never sent.
	ErrDesync = errors.New("sequence desynchronised; reopen the session")
	// ErrNoSession is the board reporting it lost the session (it reset).
	ErrNoSession = errors.New("board reset since HELLO; reopen the session")
	// ErrTooManyKeys refuses a seventh simultaneous key, which the board
	// would drop.
	ErrTooManyKeys = errors.New("six keys are already held")
	ErrSyncLost    = errors.New("execution could not be confirmed: SYNC completion was lost or the queue was cleared")
)

// Held is the host's model of what the board holds down, kept from the
// commands it sent and cleared by EVENT_RELEASED.
type Held struct {
	Modifiers      uint8
	Keys           [6]uint8
	MouseButtons   uint8
	PointerButtons uint8
	Consumer       uint16
}

// Any reports whether anything is held.
func (h Held) Any() bool {
	return h.Modifiers != 0 || h.KeyCount() != 0 || h.MouseButtons != 0 || h.PointerButtons != 0 || h.Consumer != 0
}

// KeyCount is the number of non-modifier keys held.
func (h Held) KeyCount() int {
	n := 0
	for _, k := range h.Keys {
		if k != 0 {
			n++
		}
	}
	return n
}

func (h Held) hasKey(u uint8) bool {
	for _, k := range h.Keys {
		if k == u {
			return true
		}
	}
	return false
}

// apply updates the model for a command the host is about to send.
func (h *Held) apply(m proto.Message) error {
	switch m.Type {
	case proto.TypeKeyDown:
		u := m.Body[0]
		if keymap.IsModifier(u) {
			h.Modifiers |= keymap.ModifierBit(u)
			return nil
		}
		if h.hasKey(u) {
			return nil
		}
		for i, k := range h.Keys {
			if k == 0 {
				h.Keys[i] = u
				return nil
			}
		}
		return ErrTooManyKeys
	case proto.TypeKeyUp:
		u := m.Body[0]
		if keymap.IsModifier(u) {
			h.Modifiers &^= keymap.ModifierBit(u)
			return nil
		}
		for i, k := range h.Keys {
			if k == u {
				h.Keys[i] = 0
			}
		}
	case proto.TypeKeyReport:
		h.Modifiers = m.Body[0]
		copy(h.Keys[:], m.Body[2:8])
	case proto.TypeMouse:
		h.MouseButtons = m.Body[0]
	case proto.TypePointer:
		h.PointerButtons = m.Body[0]
	case proto.TypeConsumer:
		if m.Body[2] != 0 {
			h.Consumer = uint16(m.Body[0]) | uint16(m.Body[1])<<8
		} else {
			h.Consumer = 0
		}
	case proto.TypeReleaseAll, proto.TypeHello, proto.TypeUSBDetach, proto.TypeBootloader:
		*h = Held{}
	}
	return nil
}

// Stats counts what the link did.
type Stats struct {
	Sent          int `json:"sent"`
	Retransmits   int `json:"retransmits"`
	Acks          int `json:"acks"`
	Naks          int `json:"naks"`
	QueueFull     int `json:"queue_full"`
	DecodeErrors  int `json:"decode_errors"`
	Events        int `json:"events"`
	EventGaps     int `json:"event_gaps"`
	EventsDropped int `json:"events_dropped"`
}

// Pending is a submitted command whose acknowledgement may be awaited.
type Pending struct {
	Seq  uint8
	Type uint8

	wire       []byte
	sent       time.Time
	tries      int
	requeue    time.Time
	done       chan struct{}
	ack        []byte
	err        error
	completed  bool
	message    proto.Message
	ignoreHeld bool
}

// Wait blocks until the command is acknowledged, refused, or ctx ends.
func (p *Pending) Wait(ctx context.Context) ([]byte, error) {
	select {
	case <-p.done:
		return p.ack, p.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Session is one open session with a board.
type Session struct {
	port  Port
	modem ModemStatus
	opts  Options
	log   *slog.Logger
	hello proto.HelloInfo

	// sendMu orders sequence assignment with the first write of a frame so
	// the wire carries sequence numbers in order; writeMu serialises every
	// write, retransmissions included.
	sendMu  chan struct{}
	writeMu sync.Mutex

	mu           sync.Mutex
	nextSeq      uint8
	seqChange    chan struct{}
	lastWire     []byte
	pending      []*Pending
	window       chan struct{}
	failed       error
	closing      bool
	held         Held
	acceptedHeld Held
	workUntil    time.Time
	stats        Stats
	eventSeen    bool
	eventNext    uint8
	doneWaiters  map[uint8]chan error
	subscribers  map[int]chan proto.Event
	nextSub      int

	stop      chan struct{}
	hbStop    chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Open starts a session: it resynchronises the stream, sends HELLO, checks
// the board's protocol version and starts the heartbeat.
func Open(port Port, opts Options) (*Session, error) {
	return OpenContext(context.Background(), port, opts)
}

// OpenContext allows an interrupted handshake to release the port promptly.
func OpenContext(ctx context.Context, port Port, opts Options) (*Session, error) {
	opts = opts.withDefaults()
	s := &Session{
		port:        port,
		opts:        opts,
		log:         opts.Logger,
		window:      make(chan struct{}, proto.Window),
		sendMu:      make(chan struct{}, 1),
		seqChange:   make(chan struct{}),
		doneWaiters: map[uint8]chan error{},
		subscribers: map[int]chan proto.Event{},
		stop:        make(chan struct{}),
		hbStop:      make(chan struct{}),
	}
	s.modem, _ = port.(ModemStatus)
	s.wg.Add(2)
	go s.reader()
	go s.timer()
	if _, err := s.write([]byte{frame.Delimiter}); err != nil {
		s.abort()
		return nil, err
	}
	ack, err := s.SendContext(ctx, proto.Hello())
	if err != nil {
		s.abort()
		return nil, fmt.Errorf("hello: %w", err)
	}
	if s.hello, err = proto.ParseHello(ack); err != nil {
		s.abort()
		return nil, err
	}
	if opts.Heartbeat > 0 {
		s.wg.Add(1)
		go s.heartbeat()
	}
	return s, nil
}

// Hello returns what the board answered to HELLO.
func (s *Session) Hello() proto.HelloInfo { return s.hello }

// Held returns the host's model of what is held down.
func (s *Session) Held() Held {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// Validate checks a whole command against the current held model before
// any of it is sent, so an invalid chord cannot leave a partial chord held.
func (s *Session) Validate(messages []proto.Message) error {
	held := s.Held()
	for _, m := range messages {
		if want, ok := proto.BodyLength[m.Type]; !ok || len(m.Body) != want {
			return fmt.Errorf("invalid %s body", proto.TypeName(m.Type))
		}
		if err := held.apply(m); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns the link counters.
func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// CTS reports the modem status, or serial.ErrNoModemStatus.
func (s *Session) CTS() (bool, error) {
	if s.modem == nil {
		return false, serial.ErrNoModemStatus
	}
	return s.modem.CTS()
}

// Err returns the sticky failure that ended the session, if any.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Done closes when the session closes or the link fails.
func (s *Session) Done() <-chan struct{} { return s.stop }

// Submit sends a command without waiting for its acknowledgement. It blocks
// while the window is full.
func (s *Session) Submit(ctx context.Context, m proto.Message) (*Pending, error) {
	return s.submit(ctx, m, nil)
}

// Send submits a command and waits for its acknowledgement body.
func (s *Session) Send(m proto.Message) ([]byte, error) {
	return s.SendContext(context.Background(), m)
}

// SendContext is Send bounded by ctx.
func (s *Session) SendContext(ctx context.Context, m proto.Message) ([]byte, error) {
	p, err := s.Submit(ctx, m)
	if err != nil {
		return nil, err
	}
	return p.Wait(ctx)
}

func (s *Session) submit(ctx context.Context, m proto.Message, assigned func(seq uint8)) (*Pending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if want, ok := proto.BodyLength[m.Type]; !ok || len(m.Body) != want {
		return nil, fmt.Errorf("%s with a body of %d bytes", proto.TypeName(m.Type), len(m.Body))
	}
	select {
	case s.window <- struct{}{}:
	case <-s.stop:
		return nil, s.closedErr()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case s.sendMu <- struct{}{}:
	case <-ctx.Done():
		<-s.window
		return nil, ctx.Err()
	case <-s.stop:
		<-s.window
		return nil, s.closedErr()
	}
	defer func() { <-s.sendMu }()
	if err := ctx.Err(); err != nil {
		<-s.window
		return nil, err
	}
	s.mu.Lock()
	for s.doneWaiters[s.nextSeq] != nil && s.failed == nil {
		changed := s.seqChange
		s.mu.Unlock()
		select {
		case <-changed:
		case <-s.stop:
		case <-ctx.Done():
			<-s.window
			return nil, ctx.Err()
		}
		s.mu.Lock()
	}
	if s.failed != nil {
		err := s.failed
		s.mu.Unlock()
		<-s.window
		return nil, err
	}
	if s.closing && m.Type != proto.TypeReleaseAll {
		s.mu.Unlock()
		<-s.window
		return nil, ErrClosed
	}
	held := s.held
	if err := held.apply(m); err != nil {
		s.mu.Unlock()
		<-s.window
		return nil, err
	}
	wire, err := frame.Encode(frame.Frame{Seq: s.nextSeq, Type: m.Type, Body: m.Body})
	if err != nil {
		s.mu.Unlock()
		<-s.window
		return nil, err
	}
	m.Body = append([]byte(nil), m.Body...)
	p := &Pending{Seq: s.nextSeq, Type: m.Type, message: m, wire: wire, sent: time.Now(), done: make(chan struct{})}
	s.lastWire = wire
	if m.Type == proto.TypeWait {
		if s.workUntil.Before(time.Now()) {
			s.workUntil = time.Now()
		}
		s.workUntil = s.workUntil.Add(time.Duration(uint16(m.Body[0])|uint16(m.Body[1])<<8) * time.Millisecond)
	}
	if m.Type == proto.TypeReleaseAll || m.Type == proto.TypeUSBDetach || m.Type == proto.TypeHello {
		s.workUntil = time.Time{}
	}
	s.nextSeq++
	s.held = held
	s.pending = append(s.pending, p)
	s.stats.Sent++
	if assigned != nil {
		assigned(p.Seq)
	}
	s.mu.Unlock()
	if _, err = s.write(wire); err != nil {
		s.fail(err)
		return nil, err
	}
	return p, nil
}

func (s *Session) closedErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	return ErrClosed
}

func (s *Session) write(b []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.port.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return n, err
}

// Sync submits SYNC and waits for the board to reach it in its queue.
func (s *Session) Sync(ctx context.Context) error {
	ch := make(chan error, 1)
	var seq uint8
	var budget time.Duration
	p, err := s.submit(ctx, proto.Sync(), func(assigned uint8) {
		seq = assigned
		s.doneWaiters[seq] = ch
		budget = max(time.Until(s.workUntil), 0) + s.opts.AckTimeout*time.Duration(s.opts.Retries+1) + 2*time.Second
	})
	if err != nil {
		return err
	}
	defer func() {
		s.mu.Lock()
		if s.doneWaiters[seq] == ch {
			delete(s.doneWaiters, seq)
			close(s.seqChange)
			s.seqChange = make(chan struct{})
		}
		s.mu.Unlock()
	}()
	if _, err = p.Wait(ctx); err != nil {
		return err
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case err := <-ch:
		return err
	case <-timer.C:
		return ErrSyncLost
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return s.closedErr()
	}
}

// Status asks the board for its status.
func (s *Session) Status() (proto.StatusInfo, error) {
	return s.StatusContext(context.Background())
}

func (s *Session) StatusContext(ctx context.Context) (proto.StatusInfo, error) {
	ack, err := s.SendContext(ctx, proto.Status())
	if err != nil {
		return proto.StatusInfo{}, err
	}
	return proto.ParseStatus(ack)
}

// Identity asks the board who it is.
func (s *Session) Identity() (string, error) {
	return s.IdentityContext(context.Background())
}

func (s *Session) IdentityContext(ctx context.Context) (string, error) {
	ack, err := s.SendContext(ctx, proto.Identity())
	if err != nil {
		return "", err
	}
	return proto.ParseIdentity(ack)
}

// Subscribe returns a channel of board events. Events arriving while the
// channel is full are dropped and counted. The returned function
// unsubscribes.
func (s *Session) Subscribe(buffer int) (<-chan proto.Event, func()) {
	ch := make(chan proto.Event, buffer)
	s.mu.Lock()
	if s.failed != nil {
		close(ch)
		s.mu.Unlock()
		return ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	s.subscribers[id] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(ch) // under s.mu, so no send can race the close
		}
		s.mu.Unlock()
	}
}

// Abandon ends the session without the RELEASE_ALL courtesy, for use after
// BOOTLOADER, when the board has left the protocol and cannot answer.
func (s *Session) Abandon() {
	s.closeOnce.Do(func() {
		close(s.hbStop)
		s.abort()
	})
}

// Close releases everything the session holds, stops the heartbeat and
// closes the port. It is safe to call more than once.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(s.hbStop)
		if s.Err() == nil {
			ctx, cancel := context.WithTimeout(context.Background(), s.opts.AckTimeout)
			if _, e := s.SendContext(ctx, proto.ReleaseAll()); e != nil && !errors.Is(e, ErrClosed) {
				s.closeErr = fmt.Errorf("release all on close: %w", e)
				s.log.Warn("release all on close failed", "err", e)
			}
			cancel()
		}
		s.abort()
	})
	return s.closeErr
}

// abort stops the goroutines, fails every pending command and closes the
// port without the RELEASE_ALL courtesy.
func (s *Session) abort() {
	s.mu.Lock()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	if s.failed == nil {
		s.failed = ErrClosed
	}
	s.failPendingLocked(s.failed)
	s.mu.Unlock()
	_ = s.port.Close()
	s.wg.Wait()
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.failed == nil {
		s.failed = err
		s.log.Warn("link failed", "err", err)
	}
	s.failPendingLocked(err)
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.mu.Unlock()
}

func (s *Session) failPendingLocked(err error) {
	for _, p := range s.pending {
		if !p.completed {
			p.err = err
			close(p.done)
		}
		<-s.window
	}
	s.pending = nil
	for id, ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, id)
	}
}

func (s *Session) reader() {
	defer s.wg.Done()
	buf := make([]byte, 512)
	var dec frame.Decoder
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		n, err := s.port.ReadTimeout(buf, 200*time.Millisecond)
		if n > 0 {
			dec.Push(buf[:n], s.handle)
		}
		if errors.Is(err, serial.ErrTimeout) {
			continue
		}
		if err != nil {
			select {
			case <-s.stop:
			default:
				s.fail(fmt.Errorf("read: %w", err))
			}
			return
		}
	}
}

func (s *Session) handle(f frame.Frame, err error) {
	if err != nil {
		s.mu.Lock()
		s.stats.DecodeErrors++
		s.mu.Unlock()
		s.log.Debug("undecodable frame from board", "err", err)
		return
	}
	switch {
	case f.Type == proto.TypeACK:
		s.handleACK(f)
	case f.Type == proto.TypeNAK:
		s.handleNAK(f)
	case proto.IsEvent(f.Type):
		s.handleEvent(f)
	default:
		s.log.Debug("unexpected frame type from board", "type", proto.TypeName(f.Type))
	}
}

func (s *Session) completeLocked(i int, ack []byte, err error) {
	p := s.pending[i]
	if p.completed {
		return
	}
	p.ack, p.err = ack, err
	p.completed = true
	close(p.done)
	// Retire only a contiguous prefix. Otherwise one lost ACK lets newer
	// commands advance beyond the board's eight-entry duplicate window.
	for len(s.pending) > 0 && s.pending[0].completed {
		p := s.pending[0]
		if p.err == nil && !p.ignoreHeld {
			_ = s.acceptedHeld.apply(p.message)
		}
		s.pending = s.pending[1:]
		<-s.window
	}
	s.rebuildHeldLocked()
}

func (s *Session) rebuildHeldLocked() {
	s.held = s.acceptedHeld
	for _, p := range s.pending {
		if p.err == nil && !p.ignoreHeld {
			_ = s.held.apply(p.message)
		}
	}
}

func (s *Session) handleACK(f frame.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Acks++
	for i, p := range s.pending {
		if p.Seq == f.Seq {
			if !validACK(p.Type, f.Body) {
				s.stats.DecodeErrors++
				return
			}
			s.completeLocked(i, f.Body, nil)
			return
		}
	}
	s.log.Debug("acknowledgement for no pending frame", "seq", f.Seq)
}

func validACK(kind uint8, body []byte) bool {
	switch kind {
	case proto.TypeHello:
		return len(body) == 4
	case proto.TypeStatus:
		return len(body) == proto.StatusLength
	case proto.TypeIdentity:
		_, err := proto.ParseIdentity(body)
		return err == nil
	case proto.TypeWakeup:
		return len(body) == 1 && body[0] <= 1
	default:
		return len(body) == 0
	}
}

func (s *Session) handleNAK(f frame.Frame) {
	nak, err := proto.ParseNAK(f.Body)
	if err != nil {
		s.log.Debug("bad NAK", "err", err)
		return
	}
	var failure error
	s.mu.Lock()
	s.stats.Naks++
	switch nak.Code {
	case proto.NAKFraming, proto.NAKCRC:
		// The board could not read the sequence number: the oldest frame
		// in flight is the one it most likely mangled.
		if len(s.pending) > 0 {
			s.pending[0].tries++
			if s.pending[0].tries > s.opts.Retries {
				failure = ErrLinkLost
				break
			}
			s.stats.Retransmits++
			// Let the writer timer resend. Writing from the reader can
			// deadlock a full-duplex stream when both ends are writing.
			s.pending[0].requeue = time.Now()
		}
	case proto.NAKSequence:
		// Go back to the frame the board expects, if it is ours to resend.
		// The resend is deferred by the retry pause rather than immediate:
		// a frame behind one the board refused with QUEUE_FULL draws this
		// NAK until that frame gets in, and answering every NAK with an
		// immediate resend would flood the link.
		from := -1
		for i, p := range s.pending {
			if p.Seq == nak.Expected {
				from = i
				break
			}
		}
		if from < 0 {
			if nak.Expected == s.nextSeq && len(s.pending) == 0 {
				break // a stale duplicate the board ignored; nothing is outstanding
			}
			failure = fmt.Errorf("%w: board expects %d, host has %d..%d in flight", ErrDesync, nak.Expected, s.firstSeqLocked(), s.nextSeq-1)
			break
		}
		s.stats.Retransmits++
		s.deferLocked(from)
	case proto.NAKQueueFull:
		s.stats.QueueFull++
		for i, p := range s.pending {
			if p.Seq == f.Seq {
				s.deferLocked(i)
				break
			}
		}
	case proto.NAKNoSession:
		failure = ErrNoSession
	default:
		for i, p := range s.pending {
			if p.Seq == f.Seq {
				s.completeLocked(i, nil, &proto.Error{Type: p.Type, Seq: p.Seq, Code: nak.Code, Expected: nak.Expected})
				break
			}
		}
	}
	s.mu.Unlock()
	if failure != nil {
		s.fail(failure)
		return
	}
}

// retryPause is how long the host waits before sending a refused frame
// again. The frames behind it are deferred with it, so they go out in order
// once the board has room.
const retryPause = 20 * time.Millisecond

// deferLocked schedules pending[from:] for resending after retryPause,
// keeping an earlier schedule where one exists. s.mu must be held.
func (s *Session) deferLocked(from int) {
	due := time.Now().Add(retryPause)
	for _, p := range s.pending[from:] {
		if p.completed {
			continue
		}
		if p.requeue.IsZero() || due.Before(p.requeue) {
			p.requeue = due
		}
	}
}

func (s *Session) firstSeqLocked() uint8 {
	if len(s.pending) == 0 {
		return s.nextSeq
	}
	return s.pending[0].Seq
}

func (s *Session) handleEvent(f frame.Frame) {
	e, err := proto.ParseEvent(f)
	if err != nil {
		s.log.Debug("bad event", "err", err)
		return
	}
	s.mu.Lock()
	s.stats.Events++
	if s.eventSeen && e.Counter != s.eventNext {
		s.stats.EventGaps++
		s.log.Warn("board events lost", "expected", s.eventNext, "got", e.Counter)
	}
	s.eventSeen, s.eventNext = true, e.Counter+1
	switch e.Type {
	case proto.TypeEventDone:
		if ch, ok := s.doneWaiters[e.DoneSeq]; ok {
			select {
			case ch <- nil:
			default:
			}
		}
	case proto.TypeEventReleased:
		s.acceptedHeld = Held{}
		for _, p := range s.pending {
			p.ignoreHeld = true
			if p.Type == proto.TypeReleaseAll || p.Type == proto.TypeHello || p.Type == proto.TypeUSBDetach {
				break
			}
		}
		s.rebuildHeldLocked()
		for _, ch := range s.doneWaiters {
			select {
			case ch <- ErrSyncLost:
			default:
			}
		}
	}
	for _, ch := range s.subscribers {
		select {
		case ch <- e:
		default:
			s.stats.EventsDropped++
		}
	}
	s.mu.Unlock()
}

// timer retransmits overdue frames and re-sends frames the board's queue
// could not take. Sustained CTS loss ends the session with a diagnostic.
func (s *Session) timer() {
	defer s.wg.Done()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		now := time.Now()
		var resend [][]byte
		var failure error
		s.mu.Lock()
		for _, p := range s.pending {
			if p.completed {
				continue
			}
			if !p.requeue.IsZero() && !now.Before(p.requeue) {
				p.requeue = time.Time{}
				p.sent = now
				resend = append(resend, p.wire)
			}
		}
		if len(s.pending) > 0 && len(resend) == 0 {
			oldest := s.pending[0]
			if now.Sub(oldest.sent) > s.opts.AckTimeout {
				if !s.ctsAsserted() {
					failure = fmt.Errorf("%w: CTS# deasserted; TARGET side stopped responding", ErrLinkLost)
				} else {
					oldest.tries++
					if oldest.tries > s.opts.Retries {
						failure = fmt.Errorf("%w: %s seq %d", ErrLinkLost, proto.TypeName(oldest.Type), oldest.Seq)
					} else {
						s.stats.Retransmits++
						s.log.Debug("retransmit", "from_seq", oldest.Seq, "try", oldest.tries)
						for _, p := range s.pending {
							if p.completed {
								continue
							}
							p.sent = now
							resend = append(resend, p.wire)
						}
					}
				}
			}
		}
		s.mu.Unlock()
		if failure != nil {
			s.fail(failure)
			return
		}
		for _, wire := range resend {
			if _, err := s.write(wire); err != nil {
				s.fail(err)
				return
			}
		}
	}
}

// ctsAsserted is true unless the port can read CTS and it is deasserted.
func (s *Session) ctsAsserted() bool {
	if s.modem == nil {
		return true
	}
	cts, err := s.modem.CTS()
	if err != nil {
		return true
	}
	return cts
}

// heartbeat keeps the board's watchdog fed even while an earlier response
// is missing. Duplicates feed the watchdog without consuming a new sequence.
func (s *Session) heartbeat() {
	defer s.wg.Done()
	tick := time.NewTicker(s.opts.Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-s.hbStop:
			return
		case <-s.stop:
			return
		case <-tick.C:
		}
		s.mu.Lock()
		room := len(s.pending) < proto.Window && s.failed == nil
		var repeat []byte
		if s.doneWaiters[s.nextSeq] != nil {
			room = false
			repeat = s.lastWire
		} else if len(s.pending) == proto.Window {
			repeat = s.pending[0].wire
		}
		for _, p := range s.pending {
			if p.Type == proto.TypeHeartbeat && !p.completed {
				room = false
				repeat = s.pending[0].wire
			}
		}
		if len(repeat) > 0 {
			s.stats.Retransmits++
		}
		s.mu.Unlock()
		if len(repeat) > 0 {
			if _, err := s.write(repeat); err != nil {
				s.fail(err)
				return
			}
			continue
		}
		if !room {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.Heartbeat)
		if _, err := s.Submit(ctx, proto.Heartbeat()); err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, context.DeadlineExceeded) {
			s.log.Debug("heartbeat", "err", err)
		}
		cancel()
	}
}
