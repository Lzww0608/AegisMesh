package registry

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

type FileRegistrySyncMode string

const (
	FileRegistrySyncAlways FileRegistrySyncMode = "always"
	FileRegistrySyncBatch  FileRegistrySyncMode = "batch"
)

type FileRegistryV2Option func(*fileRegistryV2Config)

type fileRegistryV2Config struct {
	syncMode      FileRegistrySyncMode
	flushInterval time.Duration
	flushRecords  int
	flushBytes    int
	compactBytes  int64
	requestBuffer int
}

func defaultFileRegistryV2Config() fileRegistryV2Config {
	return fileRegistryV2Config{
		syncMode:      FileRegistrySyncBatch,
		flushInterval: 2 * time.Millisecond,
		flushRecords:  64,
		flushBytes:    64 * 1024,
		compactBytes:  16 * 1024 * 1024,
		requestBuffer: 1024,
	}
}

func WithFileRegistryV2SyncMode(mode FileRegistrySyncMode) FileRegistryV2Option {
	return func(cfg *fileRegistryV2Config) {
		cfg.syncMode = mode
	}
}

func WithFileRegistryV2GroupCommit(records int, bytes int, interval time.Duration) FileRegistryV2Option {
	return func(cfg *fileRegistryV2Config) {
		if records > 0 {
			cfg.flushRecords = records
		}
		if bytes > 0 {
			cfg.flushBytes = bytes
		}
		if interval > 0 {
			cfg.flushInterval = interval
		}
	}
}

func WithFileRegistryV2CompactBytes(bytes int64) FileRegistryV2Option {
	return func(cfg *fileRegistryV2Config) {
		cfg.compactBytes = bytes
	}
}

type FileRegistryV2 struct {
	memory       *MemoryRegistry
	snapshotPath string
	walPath      string
	now          func() time.Time
	wal          *fileRegistryWAL
	cfg          fileRegistryV2Config

	writes    sync.RWMutex
	compactMu sync.Mutex
}

func NewFileRegistryV2(path string, now func() time.Time, opts ...FileRegistryV2Option) (*FileRegistryV2, error) {
	if path == "" {
		return nil, ErrInvalidInstance
	}
	if now == nil {
		now = time.Now
	}
	cfg := defaultFileRegistryV2Config()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.syncMode == "" {
		cfg.syncMode = FileRegistrySyncBatch
	}
	if cfg.syncMode != FileRegistrySyncAlways && cfg.syncMode != FileRegistrySyncBatch {
		return nil, ErrInvalidInstance
	}
	if cfg.flushInterval <= 0 {
		cfg.flushInterval = 2 * time.Millisecond
	}
	if cfg.flushRecords <= 0 {
		cfg.flushRecords = 64
	}
	if cfg.flushBytes <= 0 {
		cfg.flushBytes = 64 * 1024
	}
	if cfg.requestBuffer <= 0 {
		cfg.requestBuffer = 1024
	}

	reg := &FileRegistryV2{
		memory:       NewMemoryRegistry(now),
		snapshotPath: path,
		walPath:      path + ".wal",
		now:          now,
		cfg:          cfg,
	}
	if err := reg.loadSnapshot(); err != nil {
		return nil, err
	}
	if err := reg.replayWAL(); err != nil {
		return nil, err
	}
	reg.memory.SweepExpired(context.Background())

	wal, err := openFileRegistryWAL(reg.walPath, cfg)
	if err != nil {
		return nil, err
	}
	reg.wal = wal
	if cfg.compactBytes > 0 && wal.Size() >= cfg.compactBytes {
		if err := reg.Compact(context.Background()); err != nil {
			_ = wal.Close()
			return nil, err
		}
	}
	return reg, nil
}

func (r *FileRegistryV2) PersistencePath() string {
	return r.snapshotPath
}

func (r *FileRegistryV2) WALPath() string {
	return r.walPath
}

func (r *FileRegistryV2) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if inst.ID == "" || inst.Service == "" || inst.Address == "" || ttl <= 0 {
		return ErrInvalidInstance
	}
	if inst.Status == status.Unspecified {
		inst.Status = InstanceHealthy
	}

	now := r.now()
	record, err := newRegisterWALRecord(inst, ttl, now)
	if err != nil {
		return err
	}

	r.writes.RLock()
	err = r.wal.Append(ctx, record)
	if err == nil {
		err = r.memory.registerAt(context.Background(), inst, ttl, now)
	}
	r.writes.RUnlock()
	if err != nil {
		return err
	}
	return r.maybeCompact(ctx)
}

func (r *FileRegistryV2) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == "" || id == "" || ttl <= 0 {
		return ErrInvalidInstance
	}

	now := r.now()
	record := newHeartbeatWALRecord(service, id, ttl, now)

	r.writes.RLock()
	if !r.memory.recordExists(service, id) {
		r.writes.RUnlock()
		return ErrInstanceNotFound
	}
	err := r.wal.Append(ctx, record)
	if err == nil {
		err = r.memory.heartbeatAt(context.Background(), service, id, ttl, now)
	}
	r.writes.RUnlock()
	if err != nil {
		return err
	}
	return r.maybeCompact(ctx)
}

func (r *FileRegistryV2) List(ctx context.Context, service string) ([]Instance, error) {
	return r.memory.List(ctx, service)
}

func (r *FileRegistryV2) Snapshot(ctx context.Context, service string) (InstanceSnapshot, error) {
	return r.memory.Snapshot(ctx, service)
}

func (r *FileRegistryV2) Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error) {
	return r.memory.Watch(ctx, service, afterVersion)
}

func (r *FileRegistryV2) SweepExpired(ctx context.Context) int {
	r.writes.RLock()
	expired := r.memory.sweepExpiredRecords(ctx)
	for _, rec := range expired {
		_ = r.wal.Append(context.Background(), newDeleteExpiredWALRecord(rec.service, rec.id, r.now()))
	}
	r.writes.RUnlock()
	if len(expired) > 0 {
		_ = r.maybeCompact(context.Background())
	}
	return len(expired)
}

func (r *FileRegistryV2) Compact(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.compactMu.Lock()
	defer r.compactMu.Unlock()

	r.writes.Lock()
	defer r.writes.Unlock()

	if err := writeFileRegistrySnapshotFile(r.snapshotPath, snapshotMemoryRegistry(r.memory)); err != nil {
		return err
	}
	return r.wal.Reset(ctx, newSnapshotMarkWALRecord(r.now()))
}

func (r *FileRegistryV2) Close() error {
	if r.wal == nil {
		return nil
	}
	return r.wal.Close()
}

func (r *FileRegistryV2) maybeCompact(ctx context.Context) error {
	if r.cfg.compactBytes <= 0 || r.wal.Size() < r.cfg.compactBytes {
		return nil
	}
	return r.Compact(ctx)
}

func (r *FileRegistryV2) loadSnapshot() error {
	raw, err := os.ReadFile(r.snapshotPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(raw) == 0 {
		return nil
	}

	var snapshot fileRegistrySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	for _, rec := range snapshot.Records {
		r.memory.restoreHistorical(rec.Instance, rec.ExpiresAt)
	}
	return nil
}

func (r *FileRegistryV2) replayWAL() error {
	result, err := replayFileRegistryWAL(r.walPath, r.applyWALRecord)
	if err != nil {
		return err
	}
	if result.truncated {
		return os.Truncate(r.walPath, result.validBytes)
	}
	return nil
}

func (r *FileRegistryV2) applyWALRecord(record fileRegistryWALRecord) error {
	switch record.op {
	case fileRegistryWALOpRegister:
		payload := fileRegistryRegisterPayload{}
		if len(record.payload) > 0 {
			if err := json.Unmarshal(record.payload, &payload); err != nil {
				return err
			}
		}
		statusCode := status.Parse(payload.Status)
		if statusCode == status.Unspecified {
			statusCode = InstanceHealthy
		}
		inst := Instance{
			ID:       record.id,
			Service:  record.service,
			Address:  record.address,
			Status:   statusCode,
			Labels:   payload.Labels,
			LastSeen: record.timestamp,
		}
		r.memory.restoreHistorical(inst, record.timestamp.Add(record.ttl))
	case fileRegistryWALOpHeartbeat:
		if err := r.memory.heartbeatAt(context.Background(), record.service, record.id, record.ttl, record.timestamp); err != nil && !errors.Is(err, ErrInstanceNotFound) {
			return err
		}
	case fileRegistryWALOpDeleteExpired:
		r.memory.deleteRecord(record.service, record.id, record.timestamp)
	case fileRegistryWALOpSnapshotMark:
		return nil
	default:
		return errInvalidWALRecord
	}
	return nil
}

func snapshotMemoryRegistry(memory *MemoryRegistry) fileRegistrySnapshot {
	now := memory.now()
	records := make([]fileRegistryRecord, 0)
	memory.services.Range(func(_, value any) bool {
		state := value.(*serviceState)
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, rec := range state.records {
			if !rec.expiresAt.After(now) {
				continue
			}
			inst := rec.instance
			inst.Labels = cloneLabels(inst.Labels)
			records = append(records, fileRegistryRecord{Instance: inst, ExpiresAt: rec.expiresAt})
		}
		return true
	})
	sort.Slice(records, func(i, j int) bool {
		if records[i].Instance.Service != records[j].Instance.Service {
			return records[i].Instance.Service < records[j].Instance.Service
		}
		return records[i].Instance.ID < records[j].Instance.ID
	})
	return fileRegistrySnapshot{Records: records}
}

func writeFileRegistrySnapshotFile(path string, snapshot fileRegistrySnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type fileRegistryWALOp uint8

const (
	fileRegistryWALOpRegister fileRegistryWALOp = iota + 1
	fileRegistryWALOpHeartbeat
	fileRegistryWALOpDeleteExpired
	fileRegistryWALOpSnapshotMark
)

const (
	fileRegistryWALMagic      uint32 = 0x41475257 // AGRW
	fileRegistryWALVersion    uint8  = 1
	fileRegistryWALHeaderSize        = 32
	fileRegistryWALCRCSize           = 4
	maxFileRegistryPayloadLen        = 1 << 20
)

var (
	fileRegistryWALCRCTable = crc32.MakeTable(crc32.Castagnoli)
	errInvalidWALRecord     = errors.New("invalid registry wal record")
)

type fileRegistryRegisterPayload struct {
	Status string            `json:"status,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type fileRegistryWALRecord struct {
	op        fileRegistryWALOp
	timestamp time.Time
	service   string
	id        string
	address   string
	ttl       time.Duration
	payload   []byte

	serviceLen int
	idLen      int
	addressLen int
	payloadLen int
}

func newRegisterWALRecord(inst Instance, ttl time.Duration, now time.Time) (fileRegistryWALRecord, error) {
	payload, err := json.Marshal(fileRegistryRegisterPayload{Status: inst.Status.String(), Labels: inst.Labels})
	if err != nil {
		return fileRegistryWALRecord{}, err
	}
	return fileRegistryWALRecord{
		op:        fileRegistryWALOpRegister,
		timestamp: now,
		service:   inst.Service,
		id:        inst.ID,
		address:   inst.Address,
		ttl:       ttl,
		payload:   payload,
	}, nil
}

func newHeartbeatWALRecord(service, id string, ttl time.Duration, now time.Time) fileRegistryWALRecord {
	return fileRegistryWALRecord{op: fileRegistryWALOpHeartbeat, timestamp: now, service: service, id: id, ttl: ttl}
}

func newDeleteExpiredWALRecord(service, id string, now time.Time) fileRegistryWALRecord {
	return fileRegistryWALRecord{op: fileRegistryWALOpDeleteExpired, timestamp: now, service: service, id: id}
}

func newSnapshotMarkWALRecord(now time.Time) fileRegistryWALRecord {
	return fileRegistryWALRecord{op: fileRegistryWALOpSnapshotMark, timestamp: now}
}

func encodeFileRegistryWALRecord(record fileRegistryWALRecord) ([]byte, error) {
	serviceLen := len(record.service)
	idLen := len(record.id)
	addressLen := len(record.address)
	payloadLen := len(record.payload)
	if serviceLen > 0xffff || idLen > 0xffff || addressLen > 0xffff || payloadLen > maxFileRegistryPayloadLen {
		return nil, errInvalidWALRecord
	}
	if record.timestamp.IsZero() {
		record.timestamp = time.Now()
	}

	bodyLen := serviceLen + idLen + addressLen + payloadLen
	out := make([]byte, fileRegistryWALHeaderSize+bodyLen+fileRegistryWALCRCSize)
	binary.BigEndian.PutUint32(out[0:4], fileRegistryWALMagic)
	out[4] = fileRegistryWALVersion
	out[5] = byte(record.op)
	binary.BigEndian.PutUint64(out[6:14], uint64(record.timestamp.UnixNano()))
	binary.BigEndian.PutUint16(out[14:16], uint16(serviceLen))
	binary.BigEndian.PutUint16(out[16:18], uint16(idLen))
	binary.BigEndian.PutUint16(out[18:20], uint16(addressLen))
	binary.BigEndian.PutUint32(out[20:24], uint32(payloadLen))
	binary.BigEndian.PutUint64(out[24:32], uint64(record.ttl))

	offset := fileRegistryWALHeaderSize
	offset += copy(out[offset:], record.service)
	offset += copy(out[offset:], record.id)
	offset += copy(out[offset:], record.address)
	offset += copy(out[offset:], record.payload)
	crc := crc32.Checksum(out[:offset], fileRegistryWALCRCTable)
	binary.BigEndian.PutUint32(out[offset:offset+fileRegistryWALCRCSize], crc)
	return out, nil
}

type fileRegistryWALReplayResult struct {
	validBytes int64
	truncated  bool
	records    int
}

func replayFileRegistryWAL(path string, apply func(fileRegistryWALRecord) error) (fileRegistryWALReplayResult, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileRegistryWALReplayResult{}, nil
		}
		return fileRegistryWALReplayResult{}, err
	}
	defer file.Close()

	result := fileRegistryWALReplayResult{}
	header := make([]byte, fileRegistryWALHeaderSize)
	for {
		_, err := io.ReadFull(file, header)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return result, nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				result.truncated = true
				return result, nil
			}
			return result, err
		}

		parsed, bodyLen, ok := parseFileRegistryWALHeader(header)
		if !ok {
			result.truncated = true
			return result, nil
		}
		bodyAndCRC := make([]byte, bodyLen+fileRegistryWALCRCSize)
		if _, err := io.ReadFull(file, bodyAndCRC); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				result.truncated = true
				return result, nil
			}
			return result, err
		}

		frameLen := fileRegistryWALHeaderSize + bodyLen
		frame := make([]byte, frameLen)
		copy(frame, header)
		copy(frame[fileRegistryWALHeaderSize:], bodyAndCRC[:bodyLen])
		expectedCRC := binary.BigEndian.Uint32(bodyAndCRC[bodyLen : bodyLen+fileRegistryWALCRCSize])
		if crc32.Checksum(frame, fileRegistryWALCRCTable) != expectedCRC {
			result.truncated = true
			return result, nil
		}
		if !populateFileRegistryWALBody(&parsed, bodyAndCRC[:bodyLen]) {
			result.truncated = true
			return result, nil
		}
		if err := apply(parsed); err != nil {
			return result, err
		}
		result.records++
		result.validBytes += int64(frameLen + fileRegistryWALCRCSize)
	}
}

func parseFileRegistryWALHeader(header []byte) (fileRegistryWALRecord, int, bool) {
	if len(header) != fileRegistryWALHeaderSize {
		return fileRegistryWALRecord{}, 0, false
	}
	if binary.BigEndian.Uint32(header[0:4]) != fileRegistryWALMagic || header[4] != fileRegistryWALVersion {
		return fileRegistryWALRecord{}, 0, false
	}
	op := fileRegistryWALOp(header[5])
	if op != fileRegistryWALOpRegister && op != fileRegistryWALOpHeartbeat && op != fileRegistryWALOpDeleteExpired && op != fileRegistryWALOpSnapshotMark {
		return fileRegistryWALRecord{}, 0, false
	}
	serviceLen := int(binary.BigEndian.Uint16(header[14:16]))
	idLen := int(binary.BigEndian.Uint16(header[16:18]))
	addressLen := int(binary.BigEndian.Uint16(header[18:20]))
	payloadLen := int(binary.BigEndian.Uint32(header[20:24]))
	if payloadLen > maxFileRegistryPayloadLen {
		return fileRegistryWALRecord{}, 0, false
	}
	if op == fileRegistryWALOpRegister && (serviceLen == 0 || idLen == 0 || addressLen == 0) {
		return fileRegistryWALRecord{}, 0, false
	}
	if (op == fileRegistryWALOpHeartbeat || op == fileRegistryWALOpDeleteExpired) && (serviceLen == 0 || idLen == 0 || addressLen != 0 || payloadLen != 0) {
		return fileRegistryWALRecord{}, 0, false
	}
	if op == fileRegistryWALOpSnapshotMark && (serviceLen != 0 || idLen != 0 || addressLen != 0 || payloadLen != 0) {
		return fileRegistryWALRecord{}, 0, false
	}

	ttl := time.Duration(binary.BigEndian.Uint64(header[24:32]))
	if (op == fileRegistryWALOpRegister || op == fileRegistryWALOpHeartbeat) && ttl <= 0 {
		return fileRegistryWALRecord{}, 0, false
	}
	if (op == fileRegistryWALOpDeleteExpired || op == fileRegistryWALOpSnapshotMark) && ttl != 0 {
		return fileRegistryWALRecord{}, 0, false
	}

	return fileRegistryWALRecord{
		op:         op,
		timestamp:  time.Unix(0, int64(binary.BigEndian.Uint64(header[6:14]))),
		ttl:        ttl,
		serviceLen: serviceLen,
		idLen:      idLen,
		addressLen: addressLen,
		payloadLen: payloadLen,
	}, serviceLen + idLen + addressLen + payloadLen, true
}

func populateFileRegistryWALBody(record *fileRegistryWALRecord, body []byte) bool {
	if len(body) != record.serviceLen+record.idLen+record.addressLen+record.payloadLen {
		return false
	}
	offset := 0
	record.service = string(body[offset : offset+record.serviceLen])
	offset += record.serviceLen
	record.id = string(body[offset : offset+record.idLen])
	offset += record.idLen
	record.address = string(body[offset : offset+record.addressLen])
	offset += record.addressLen
	if record.payloadLen > 0 {
		record.payload = append([]byte(nil), body[offset:offset+record.payloadLen]...)
	}
	return true
}

type fileRegistryWALRequestKind uint8

const (
	fileRegistryWALRequestAppend fileRegistryWALRequestKind = iota + 1
	fileRegistryWALRequestReset
	fileRegistryWALRequestClose
)

var errFileRegistryWALClosed = errors.New("registry wal is closed")

type fileRegistryWALRequest struct {
	kind fileRegistryWALRequestKind
	data []byte
	ack  chan error
}

type fileRegistryWAL struct {
	path     string
	cfg      fileRegistryV2Config
	requests chan fileRegistryWALRequest
	done     chan struct{}
	size     atomic.Int64

	submitMu sync.RWMutex
	closed   atomic.Bool
}

func openFileRegistryWAL(path string, cfg fileRegistryV2Config) (*fileRegistryWAL, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	wal := &fileRegistryWAL{
		path:     path,
		cfg:      cfg,
		requests: make(chan fileRegistryWALRequest, cfg.requestBuffer),
		done:     make(chan struct{}),
	}
	wal.size.Store(stat.Size())
	go wal.run(file)
	return wal, nil
}

func (w *fileRegistryWAL) Size() int64 {
	return w.size.Load()
}

func (w *fileRegistryWAL) Append(ctx context.Context, record fileRegistryWALRecord) error {
	data, err := encodeFileRegistryWALRecord(record)
	if err != nil {
		return err
	}
	return w.submit(ctx, fileRegistryWALRequest{kind: fileRegistryWALRequestAppend, data: data, ack: make(chan error, 1)})
}

func (w *fileRegistryWAL) Reset(ctx context.Context, mark fileRegistryWALRecord) error {
	data, err := encodeFileRegistryWALRecord(mark)
	if err != nil {
		return err
	}
	return w.submit(ctx, fileRegistryWALRequest{kind: fileRegistryWALRequestReset, data: data, ack: make(chan error, 1)})
}

func (w *fileRegistryWAL) Close() error {
	if !w.closed.CompareAndSwap(false, true) {
		<-w.done
		return nil
	}

	req := fileRegistryWALRequest{kind: fileRegistryWALRequestClose, ack: make(chan error, 1)}
	w.submitMu.Lock()
	defer w.submitMu.Unlock()
	select {
	case <-w.done:
		return nil
	case w.requests <- req:
	}
	select {
	case err := <-req.ack:
		<-w.done
		return err
	case <-w.done:
		return nil
	}
}

func (w *fileRegistryWAL) submit(ctx context.Context, req fileRegistryWALRequest) error {
	w.submitMu.RLock()
	defer w.submitMu.RUnlock()
	if w.closed.Load() {
		return errFileRegistryWALClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return errFileRegistryWALClosed
	case w.requests <- req:
	}
	return <-req.ack
}

func (w *fileRegistryWAL) run(file *os.File) {
	defer close(w.done)

	writer := bufio.NewWriterSize(file, 64*1024)
	ticker := time.NewTicker(w.cfg.flushInterval)
	defer ticker.Stop()

	var pendingAcks []chan error
	pendingRecords := 0
	pendingBytes := 0
	var fatalErr error

	ackPending := func(err error) {
		for _, ack := range pendingAcks {
			ack <- err
		}
		pendingAcks = pendingAcks[:0]
	}
	flush := func(syncFile bool) error {
		if writer.Buffered() > 0 {
			if err := writer.Flush(); err != nil {
				return err
			}
		}
		if syncFile {
			if err := file.Sync(); err != nil {
				return err
			}
		}
		pendingRecords = 0
		pendingBytes = 0
		return nil
	}
	shouldFlush := func() bool {
		return pendingRecords >= w.cfg.flushRecords || pendingBytes >= w.cfg.flushBytes
	}

	for {
		select {
		case req := <-w.requests:
			switch req.kind {
			case fileRegistryWALRequestAppend:
				if fatalErr != nil {
					req.ack <- fatalErr
					continue
				}
				_, err := writer.Write(req.data)
				if err != nil {
					fatalErr = err
					req.ack <- err
					continue
				}
				w.size.Add(int64(len(req.data)))
				pendingRecords++
				pendingBytes += len(req.data)
				if w.cfg.syncMode == FileRegistrySyncBatch {
					req.ack <- nil
				} else {
					pendingAcks = append(pendingAcks, req.ack)
				}
				if w.cfg.syncMode == FileRegistrySyncAlways || shouldFlush() {
					err := flush(true)
					if err != nil {
						fatalErr = err
					}
					ackPending(err)
				}
			case fileRegistryWALRequestReset:
				err := flush(true)
				ackPending(err)
				if err == nil {
					err = file.Close()
				}
				if err == nil {
					file, err = os.OpenFile(w.path, os.O_CREATE|os.O_RDWR|os.O_TRUNC|os.O_APPEND, 0o644)
				}
				if err == nil {
					writer.Reset(file)
					w.size.Store(0)
					_, err = writer.Write(req.data)
				}
				if err == nil {
					w.size.Add(int64(len(req.data)))
					err = flush(true)
				}
				if err != nil {
					fatalErr = err
				}
				req.ack <- err
			case fileRegistryWALRequestClose:
				err := flush(true)
				ackPending(err)
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
				req.ack <- err
				return
			}
		case <-ticker.C:
			if pendingRecords == 0 && writer.Buffered() == 0 {
				continue
			}
			err := flush(true)
			if err != nil {
				fatalErr = err
			}
			ackPending(err)
		}
	}
}
