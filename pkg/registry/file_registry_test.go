package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileRegistryPersistsInstancesAcrossRestart(t *testing.T) {
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/registry.json"

	first, err := NewFileRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	if err := first.Register(context.Background(), Instance{
		ID:      "user-a",
		Service: "user-service",
		Address: "user-a:7001",
		Labels:  map[string]string{"variant": "primary"},
	}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}

	second, err := NewFileRegistry(path, func() time.Time { return now.Add(10 * time.Second) })
	if err != nil {
		t.Fatalf("reload file registry: %v", err)
	}
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected one restored instance, got %+v", instances)
	}
	if instances[0].ID != "user-a" || instances[0].Address != "user-a:7001" || instances[0].Labels["variant"] != "primary" {
		t.Fatalf("unexpected restored instance: %+v", instances[0])
	}
}

func TestFileRegistryDoesNotRestoreExpiredInstances(t *testing.T) {
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/registry.json"

	first, err := NewFileRegistry(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	if err := first.Register(context.Background(), Instance{
		ID:      "user-a",
		Service: "user-service",
		Address: "user-a:7001",
	}, time.Second); err != nil {
		t.Fatalf("register instance: %v", err)
	}

	second, err := NewFileRegistry(path, func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatalf("reload file registry: %v", err)
	}
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected expired instance to stay hidden, got %+v", instances)
	}
}

func TestFileRegistryV2ReplaysWALRegisterAndHeartbeat(t *testing.T) {
	current := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{
		ID:      "user-a",
		Service: "user-service",
		Address: "user-a:7001",
		Labels:  map[string]string{"variant": "primary"},
	}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	current = current.Add(30 * time.Second)
	if err := first.Heartbeat(context.Background(), "user-service", "user-a", time.Minute); err != nil {
		t.Fatalf("heartbeat instance: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}

	current = current.Add(45 * time.Second)
	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload file registry v2: %v", err)
	}
	defer second.Close()

	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected heartbeat-extended instance to restore, got %+v", instances)
	}
	if instances[0].ID != "user-a" || instances[0].Labels["variant"] != "primary" {
		t.Fatalf("unexpected restored instance: %+v", instances[0])
	}
}

func TestFileRegistryV2DoesNotRestoreExpiredInstances(t *testing.T) {
	current := time.Date(2026, 6, 24, 10, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Second); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}

	current = current.Add(2 * time.Second)
	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload file registry v2: %v", err)
	}
	defer second.Close()
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected expired instance to stay hidden, got %+v", instances)
	}
	if err := second.Heartbeat(context.Background(), "user-service", "user-a", time.Minute); err != ErrInstanceNotFound {
		t.Fatalf("expected expired restored record to be purged, got %v", err)
	}
}
func TestFileRegistryV2ReplayStopsAtInvalidCRC(t *testing.T) {
	current := time.Date(2026, 6, 24, 11, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Minute); err != nil {
		t.Fatalf("register valid instance: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}
	validInfo, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat wal before corruption: %v", err)
	}

	corrupt, err := newRegisterWALRecord(Instance{ID: "user-b", Service: "user-service", Address: "user-b:7001", Status: InstanceHealthy}, time.Minute, current)
	if err != nil {
		t.Fatalf("build corrupt record: %v", err)
	}
	encoded, err := encodeFileRegistryWALRecord(corrupt)
	if err != nil {
		t.Fatalf("encode corrupt record: %v", err)
	}
	encoded[len(encoded)-1] ^= 0xff
	wal, err := os.OpenFile(path+".wal", os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open wal for corruption: %v", err)
	}
	if _, err := wal.Write(encoded); err != nil {
		_ = wal.Close()
		t.Fatalf("append corrupt record: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("close corrupt wal: %v", err)
	}

	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload file registry v2: %v", err)
	}
	defer second.Close()

	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" {
		t.Fatalf("expected only valid prefix to restore, got %+v", instances)
	}
	truncatedInfo, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat truncated wal: %v", err)
	}
	if truncatedInfo.Size() != validInfo.Size() {
		t.Fatalf("expected invalid wal tail to be truncated to %d bytes, got %d", validInfo.Size(), truncatedInfo.Size())
	}
}

func TestFileRegistryV2ReplayStopsAtTornWrite(t *testing.T) {
	current := time.Date(2026, 6, 24, 11, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Minute); err != nil {
		t.Fatalf("register valid instance: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}
	validInfo, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat wal before torn write: %v", err)
	}

	torn, err := newRegisterWALRecord(Instance{ID: "user-b", Service: "user-service", Address: "user-b:7001", Status: InstanceHealthy}, time.Minute, current)
	if err != nil {
		t.Fatalf("build torn record: %v", err)
	}
	encoded, err := encodeFileRegistryWALRecord(torn)
	if err != nil {
		t.Fatalf("encode torn record: %v", err)
	}
	wal, err := os.OpenFile(path+".wal", os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open wal for torn write: %v", err)
	}
	if _, err := wal.Write(encoded[:len(encoded)/2]); err != nil {
		_ = wal.Close()
		t.Fatalf("append torn record: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("close torn wal: %v", err)
	}

	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload file registry v2: %v", err)
	}
	defer second.Close()
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" {
		t.Fatalf("expected only valid prefix to restore, got %+v", instances)
	}
	truncatedInfo, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat truncated wal: %v", err)
	}
	if truncatedInfo.Size() != validInfo.Size() {
		t.Fatalf("expected torn wal tail to be truncated to %d bytes, got %d", validInfo.Size(), truncatedInfo.Size())
	}
}
func TestFileRegistryV2CompactWritesSnapshotAndResetsWAL(t *testing.T) {
	current := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	current = current.Add(10 * time.Second)
	if err := first.Heartbeat(context.Background(), "user-service", "user-a", time.Minute); err != nil {
		t.Fatalf("heartbeat instance: %v", err)
	}
	before, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat wal before compact: %v", err)
	}
	if err := first.Compact(context.Background()); err != nil {
		t.Fatalf("compact registry: %v", err)
	}
	after, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat wal after compact: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("expected compacted wal to shrink below %d bytes, got %d", before.Size(), after.Size())
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}

	current = current.Add(50 * time.Second)
	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload compacted registry: %v", err)
	}
	defer second.Close()
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" {
		t.Fatalf("expected compacted snapshot to restore latest instance, got %+v", instances)
	}
}

func TestFileRegistryV2AutoCompactsAtWALThreshold(t *testing.T) {
	current := time.Date(2026, 6, 24, 12, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	reg, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(1))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := reg.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected compact snapshot: %v", err)
	}

	seenSnapshotMark := false
	result, err := replayFileRegistryWAL(path+".wal", func(record fileRegistryWALRecord) error {
		if record.op == fileRegistryWALOpSnapshotMark {
			seenSnapshotMark = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replay compacted wal: %v", err)
	}
	if result.records != 1 || !seenSnapshotMark {
		t.Fatalf("expected compacted wal to contain one SNAPSHOT_MARK, got records=%d snapshot=%v", result.records, seenSnapshotMark)
	}
}
func TestFileRegistryV2CompactsOversizedWALOnStartup(t *testing.T) {
	current := time.Date(2026, 6, 24, 12, 45, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}
	before, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat wal before startup compact: %v", err)
	}

	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(1))
	if err != nil {
		t.Fatalf("reload oversized wal registry: %v", err)
	}
	defer second.Close()
	after, err := os.Stat(path + ".wal")
	if err != nil {
		t.Fatalf("stat wal after startup compact: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("expected startup compact to shrink wal below %d bytes, got %d", before.Size(), after.Size())
	}
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" {
		t.Fatalf("expected startup-compacted snapshot to restore instance, got %+v", instances)
	}
}
func TestFileRegistryV2SweepExpiredWritesDeleteRecord(t *testing.T) {
	current := time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	reg, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := reg.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Second); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	current = current.Add(2 * time.Second)
	if expired := reg.SweepExpired(context.Background()); expired != 1 {
		t.Fatalf("expected one expired instance, got %d", expired)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}

	seenDelete := false
	_, err = replayFileRegistryWAL(path+".wal", func(record fileRegistryWALRecord) error {
		if record.op == fileRegistryWALOpDeleteExpired {
			seenDelete = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replay wal: %v", err)
	}
	if !seenDelete {
		t.Fatalf("expected SweepExpired to append DELETE_EXPIRED record")
	}

	reloaded, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	defer reloaded.Close()
	instances, err := reloaded.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected expired instance to stay deleted, got %+v", instances)
	}
}

func TestFileRegistryV2BatchCloseFlushesBufferedWAL(t *testing.T) {
	current := time.Date(2026, 6, 24, 14, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncBatch), WithFileRegistryV2GroupCommit(4096, 4*1024*1024, time.Hour), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("new file registry v2: %v", err)
	}
	if err := first.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Minute); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}

	second, err := NewFileRegistryV2(path, func() time.Time { return current }, WithFileRegistryV2SyncMode(FileRegistrySyncAlways), WithFileRegistryV2CompactBytes(0))
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	defer second.Close()
	instances, err := second.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" {
		t.Fatalf("expected Close to flush batch WAL, got %+v", instances)
	}
}
