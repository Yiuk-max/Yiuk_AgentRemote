package room

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"yiuk-switchboard/internal/connlimit"
	"yiuk-switchboard/internal/protocol"
	"yiuk-switchboard/internal/session"
)

var (
	ErrRoomLimit      = fmt.Errorf("room limit reached")
	ErrPortsExhausted = fmt.Errorf("no available room port")
	ErrManagerClosed  = fmt.Errorf("room manager is shutting down")
)

type Notify func(protocol.Message) error

type Options struct {
	PortMin, PortMax, MaxRooms, MaxPlayers, MaxPending int
	PendingTimeout, ReconnectGrace, SessionIdleTimeout time.Duration
	ConnectionBudget                                   *connlimit.Budget
}

type room struct {
	id, name, host, mc, loader, token string         //base info
	port, max, players, localPort     int            //player count and port info
	hostIP                            net.IP         //host IP for local fast path
	mods                              []protocol.Mod //mod list
	owner                             Notify         //owner control connection callback
	ownerKey                          string
	graceTimer                        *time.Timer              //timer for owner reconnect grace period
	listener                          net.Listener             //TCP listener for players
	pending                           map[string]chan net.Conn //has been accepted by the room but not yet paired with the host
	active                            map[net.Conn]struct{}    //active TCP conn (player and host) for cleanup on room close
	closed                            bool
	mu                                sync.Mutex
}

type Manager struct {
	opts         Options
	mu           sync.RWMutex
	rooms        map[string]*room // room ID -> room
	closing      bool             // true if CloseAll has been called
	done         chan struct{}    // closed when closing is true
	closeOnce    sync.Once
	wg           sync.WaitGroup
	limitLogMu   sync.Mutex
	lastLimitLog time.Time
}

func NewManager(opts Options) *Manager {
	return &Manager{opts: opts, rooms: make(map[string]*room), done: make(chan struct{})}
}

func (m *Manager) Create(name, host, mc, loader string, max int, mods []protocol.Mod, localPort int, hostIP net.IP, ownerKey string, notify Notify) (protocol.Message, error) {
	if name == "" || host == "" || mc == "" || loader == "" || notify == nil || max < 1 || max > m.opts.MaxPlayers {
		return protocol.Message{}, fmt.Errorf("invalid room fields")
	} //basic checks
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing { //manager is closing, reject new room creation
		return protocol.Message{}, ErrManagerClosed
	}
	for _, existing := range m.rooms { //the owner has already created a room, reject new room creation
		if existing.ownerKey == ownerKey {
			return protocol.Message{}, fmt.Errorf("one room per control connection")
		}
	}
	if len(m.rooms) >= m.opts.MaxRooms { //reached the maximum number of rooms, reject new room creation
		return protocol.Message{}, ErrRoomLimit
	}
	// check finished,start to find an available port and create a listener
	var ln net.Listener
	var port int
	for port = m.opts.PortMin; port <= m.opts.PortMax; port++ {
		used := false
		for _, existing := range m.rooms {
			if existing.port == port {
				used = true
				break
			}
		}
		if used { //port is already in use by another room, try the next port
			continue
		}
		var err error //find an available port and create a listener
		ln, err = net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err == nil {
			break
		}
	}
	if ln == nil {
		return protocol.Message{}, ErrPortsExhausted
	}
	id, err := randomHex(8) //generate a random room ID
	if err != nil {
		_ = ln.Close()
		return protocol.Message{}, err
	}
	token, err := randomHex(16)
	if err != nil {
		_ = ln.Close()
		return protocol.Message{}, err
	}
	//create a new room and add it to the manager's room map
	r := &room{id: id, name: name, host: host, mc: mc, loader: loader, token: token, port: port, max: max,
		mods: append([]protocol.Mod(nil), mods...), owner: notify, ownerKey: ownerKey, listener: ln,
		localPort: localPort, hostIP: append(net.IP(nil), hostIP...),
		pending: make(map[string]chan net.Conn), active: make(map[net.Conn]struct{})}
	m.rooms[id] = r
	m.wg.Add(1)
	go m.accept(r) //room listener goroutine to accept incoming player connections
	slog.Info("room created", "room_id", id, "port", port, "host", host)
	return protocol.Message{T: "created", ID: id, Port: port, Token: token}, nil
}

func (m *Manager) accept(r *room) {
	defer m.wg.Done()
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetKeepAlive(true)
		}
		budget := m.opts.ConnectionBudget
		if budget != nil && !budget.TryAcquire() {
			m.logConnectionLimit()
			_ = conn.Close()
			continue
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if budget != nil {
				defer budget.Release()
			}
			m.player(r, conn)
		}()
	}
}

func (m *Manager) logConnectionLimit() {
	m.limitLogMu.Lock()
	defer m.limitLogMu.Unlock()
	if time.Since(m.lastLimitLog) >= 10*time.Second {
		slog.Warn("room connection rejected: global connection limit reached")
		m.lastLimitLog = time.Now()
	}
}

func (m *Manager) player(r *room, player net.Conn) {
	if !m.track(r, player, true) {
		_ = player.Close()
		return
	}
	if m.tryLocalForward(r, player) {
		return
	}
	sid, err := randomHex(16)
	if err != nil {
		m.untrack(r, player)
		_ = player.Close()
		return
	}
	ch := make(chan net.Conn, 1)
	//check whether satisfies the room's Limit
	r.mu.Lock()
	if r.closed || len(r.pending) >= m.opts.MaxPending || r.players+len(r.pending) >= r.max || r.owner == nil {
		r.mu.Unlock()
		m.untrack(r, player)
		_ = player.Close()
		return
	}
	r.pending[sid] = ch
	notify := r.owner
	r.mu.Unlock()
	//notify the host about the new session
	if err := notify(protocol.Message{T: "new_session", SID: sid}); err != nil {
		m.removePending(r, sid)
		m.untrack(r, player)
		_ = player.Close()
		return
	}
	//wait for the host to pair with the new session or timeout
	timer := time.NewTimer(m.opts.PendingTimeout)
	defer timer.Stop()
	select {
	case host := <-ch: //host has paired with the new session
		if paired, ok := host.(*readerConn); ok && paired.onClose != nil {
			defer paired.onClose()
		}
		if !m.track(r, host, true) {
			r.mu.Lock()
			r.players--
			r.mu.Unlock()
			_ = host.Close()
			m.untrack(r, player)
			_ = player.Close()
			return
		}
		slog.Info("session paired", "room_id", r.id, "sid", sid)
		session.Pipe(player, nil, host, nil, m.opts.SessionIdleTimeout)
		r.mu.Lock()
		r.players--
		r.mu.Unlock()
		m.untrack(r, player)
		m.untrack(r, host)
		slog.Info("session ended", "room_id", r.id, "sid", sid)
	case <-timer.C: //timeout
		r.mu.Lock()
		delete(r.pending, sid)
		var pairedHost net.Conn
		select {
		case pairedHost = <-ch:
			r.players--
		default:
		}
		r.mu.Unlock()
		if pairedHost != nil {
			_ = pairedHost.Close()
		}
		m.untrack(r, player)
		_ = player.Close()
	case <-m.done: //manager is closing
		m.removePending(r, sid)
		m.untrack(r, player)
		_ = player.Close()
	}
}

// When the relay and host control connection originate on this machine, let
// the relay connect straight to the advertised LAN server. This removes the
// Java host-side socket copier from the common local testing topology.
func (m *Manager) tryLocalForward(r *room, player net.Conn) bool {
	remote, ok := player.RemoteAddr().(*net.TCPAddr)
	if !ok || r.localPort < 1 || r.localPort > 65535 || r.hostIP == nil || !isLocalIP(r.hostIP) ||
		(!remote.IP.Equal(r.hostIP) && !remote.IP.IsLoopback()) {
		return false
	}
	r.mu.Lock()
	if r.closed || r.owner == nil || r.players+len(r.pending) >= r.max {
		r.mu.Unlock()
		return false
	}
	r.players++
	r.mu.Unlock()
	rollback := func() { r.mu.Lock(); r.players--; r.mu.Unlock() }
	budget := m.opts.ConnectionBudget
	if budget != nil && !budget.TryAcquire() {
		rollback()
		return false
	}
	local, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.localPort)), 350*time.Millisecond)
	if err != nil {
		if budget != nil {
			budget.Release()
		}
		rollback()
		return false
	}
	if tcp, ok := local.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
	}
	if !m.track(r, local, true) {
		_ = local.Close()
		if budget != nil {
			budget.Release()
		}
		rollback()
		return false
	}
	slog.Info("session using local host fast path", "room_id", r.id)
	session.Pipe(player, nil, local, nil, m.opts.SessionIdleTimeout)
	r.mu.Lock()
	r.players--
	r.mu.Unlock()
	m.untrack(r, player)
	m.untrack(r, local)
	if budget != nil {
		budget.Release()
	}
	return true
}

func isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if network, ok := addr.(*net.IPNet); ok && network.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func (m *Manager) Pair(sid string, conn net.Conn, reader io.Reader, onClose func()) bool {
	m.mu.RLock()
	rooms := make([]*room, 0, len(m.rooms))
	for _, r := range m.rooms { //
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()
	for _, r := range rooms {
		r.mu.Lock()
		ch, ok := r.pending[sid]
		if ok {
			delete(r.pending, sid) //has been accepted by the room,so remove it from the pending map and increment the player count
			r.players++
			ch <- &readerConn{Conn: conn, reader: reader, onClose: onClose}
			//two reasons to wrap the conn with readerConn:
			//1. to read the bytes that have already been read from the conn and stored in the reader, so that the host can receive the full data stream from Minecraft client without losing any bytes.
			//2. save the capability :close,ip,deadline, etc. to the host side
		}
		r.mu.Unlock()
		if ok {
			return true
		}
	}
	return false
}

type readerConn struct {
	net.Conn
	reader  io.Reader
	onClose func()
}

func (c *readerConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// CloseWrite preserves the underlying TCP half-close capability. The relay
// tunnels gameplay through this wrapper because the initial protocol frame may
// already have buffered bytes after it.
func (c *readerConn) CloseWrite() error {
	if halfCloser, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return halfCloser.CloseWrite()
	}
	return c.Conn.Close()
}

func (m *Manager) removePending(r *room, sid string) {
	r.mu.Lock()
	delete(r.pending, sid)
	r.mu.Unlock()
}
func (m *Manager) track(r *room, conn net.Conn, add bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if add {
		if r.closed {
			return false
		}
		r.active[conn] = struct{}{}
		return true
	}
	delete(r.active, conn)
	return true
}
func (m *Manager) untrack(r *room, conn net.Conn) { m.track(r, conn, false) }

func (m *Manager) List() []protocol.RoomSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]protocol.RoomSummary, 0, len(m.rooms))
	for _, r := range m.rooms {
		r.mu.Lock()
		out = append(out, protocol.RoomSummary{ID: r.id, Name: r.name, Host: r.host, Port: r.port, Players: r.players, Max: r.max, MC: r.mc, Loader: r.loader, ModCount: len(r.mods)})
		r.mu.Unlock()
	}
	return out
}

func (m *Manager) Mods(id string) ([]protocol.Mod, bool) {
	r := m.get(id)
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Mod(nil), r.mods...), true
}

func (m *Manager) Close(id, ownerKey string) bool {
	r := m.get(id)
	if r == nil {
		return false
	}
	r.mu.Lock()
	owner := r.ownerKey == ownerKey
	r.mu.Unlock()
	if !owner {
		return false
	}
	// The owner is authorized to close the room, so destroy it.
	m.destroy(r)
	return true
}

func (m *Manager) OwnerDisconnected(ownerKey string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closing {
		return
	}
	for _, r := range m.rooms {
		r.mu.Lock()
		if r.ownerKey == ownerKey && r.owner != nil && !r.closed {
			r.owner = nil
			m.wg.Add(1)
			r.graceTimer = time.AfterFunc(m.opts.ReconnectGrace, func() {
				defer m.wg.Done()
				m.destroyDisconnected(r, ownerKey)
			})
		}
		r.mu.Unlock()
	}
}

func (m *Manager) Resume(id, token, ownerKey string, notify Notify) bool {
	r := m.get(id)
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.token != token { //invalid token or room closed, reject the resume request
		return false
	}
	// A valid room token authorizes the host to take over even if the old
	// control socket is half-open and has not yet hit its heartbeat deadline.
	// The previous connection's deferred OwnerDisconnected call is fenced by
	// ownerKey below, so it cannot mark this replacement connection offline.
	if r.graceTimer != nil {
		if r.graceTimer.Stop() {
			m.wg.Done()
		}
		r.graceTimer = nil
	}
	r.ownerKey = ownerKey
	r.owner = notify
	return true
}

// get room by id,return nil if not found
func (m *Manager) get(id string) *room { m.mu.RLock(); defer m.mu.RUnlock(); return m.rooms[id] }

// destroyDisconnected expires only the owner generation that scheduled this
// timer. A timer callback can already be running when Stop returns false, so
// the owner key and disconnected state must be checked atomically with removal.
func (m *Manager) destroyDisconnected(r *room, ownerKey string) {
	m.mu.Lock()
	if m.rooms[r.id] != r {
		m.mu.Unlock()
		return
	}
	r.mu.Lock()
	if r.closed || r.owner != nil || r.ownerKey != ownerKey {
		r.mu.Unlock()
		m.mu.Unlock()
		return
	}
	delete(m.rooms, r.id)
	r.closed = true
	// The grace timer invoked this callback; its wait-group slot is released
	// by the callback defer after this function returns.
	r.graceTimer = nil
	_ = r.listener.Close()
	for sid := range r.pending {
		delete(r.pending, sid)
	}
	for conn := range r.active {
		_ = conn.Close()
	}
	r.mu.Unlock()
	m.mu.Unlock()
	slog.Info("room closed after owner reconnect timeout", "room_id", r.id)
}

func (m *Manager) destroy(r *room) {
	m.mu.Lock()
	if m.rooms[r.id] != r {
		m.mu.Unlock()
		return
	}
	delete(m.rooms, r.id)
	m.mu.Unlock()
	r.mu.Lock()
	r.closed = true
	if r.graceTimer != nil {
		if r.graceTimer.Stop() {
			m.wg.Done()
		}
		r.graceTimer = nil
	}
	_ = r.listener.Close()
	for sid := range r.pending {
		delete(r.pending, sid)
	}
	for conn := range r.active {
		_ = conn.Close()
	}
	r.mu.Unlock()
	slog.Info("room closed", "room_id", r.id)
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.closing = true
	m.closeOnce.Do(func() { close(m.done) })
	rooms := make([]*room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.Unlock()
	for _, r := range rooms {
		m.destroy(r)
	}
	m.wg.Wait()
}

func randomHex(bytesLen int) (string, error) {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
