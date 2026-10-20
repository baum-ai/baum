package hookevents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/privatefs"
)

// Record bounds: the record file rotates at MaxRecordBytes and keeps
// RecordBackups older files, so the record never exceeds
// (RecordBackups+1)*MaxRecordBytes, a few thousand events.
const (
	MaxRecordBytes = 1 << 20
	RecordBackups  = 2
)

// rememberedIDs is how many recent event ids the store keeps to record each
// event once: an event delivered again after this many others (a spooled copy
// drained very late) is recorded twice, and readers dedupe by id.
const rememberedIDs = 4096

// Delivery says how an event reached the daemon.
const (
	ViaSocket = "socket"
	ViaSpool  = "spool"
)

// Record is one event as the daemon received it.
type Record struct {
	Received time.Time `json:"received"`
	// Via is ViaSocket or ViaSpool. A spooled event arrives later than
	// events sent after it, so readers order by Event.Time.
	Via   string `json:"via"`
	Event Event  `json:"event"`
}

// Store is the daemon's local record of hook events: JSON lines in
// events.jsonl (and events.jsonl.1, .2) in a private directory, mode 0600.
// Only the daemon that holds the daemon lock opens it.
type Store struct {
	mu    sync.Mutex
	path  string
	file  *daemon.RotatingFile
	ids   map[string]bool
	order []string
	subs  map[chan Record]func(Event) bool
	log   *slog.Logger
}

// RecordDir is the directory of the event record of the daemon whose state
// directory paths name.
func RecordDir(paths daemon.Paths) string { return filepath.Join(paths.State, "events") }

// OpenStore opens the record in dir, creating it private, and remembers the
// ids it already holds.
func OpenStore(dir string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := privatefs.MkdirAll(dir); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "events.jsonl"), ids: map[string]bool{}, subs: map[chan Record]func(Event) bool{}, log: log}
	records, err := s.read()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		s.remember(record.Event.ID)
	}
	if s.file, err = daemon.OpenLog(s.path, MaxRecordBytes, RecordBackups); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) remember(id string) {
	if s.ids[id] {
		return
	}
	s.ids[id] = true
	s.order = append(s.order, id)
	if len(s.order) > rememberedIDs {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
}

// Add records e unless an event with its id was recorded recently, and hands
// the record to every subscriber. It reports whether e was new.
func (s *Store) Add(e Event, via string) (bool, error) {
	if err := e.check(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids[e.ID] {
		return false, nil
	}
	record := Record{Received: time.Now().UTC(), Via: via, Event: e}
	data, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	if _, err = s.file.Write(append(data, '\n')); err != nil {
		return false, err
	}
	s.remember(e.ID)
	for sub, match := range s.subs {
		if match != nil && !match(e) {
			continue
		}
		select {
		case sub <- record:
		default:
			s.log.Warn("a hook event subscriber fell behind and missed an event", "kind", e.Kind, "name", e.Name)
		}
	}
	return true, nil
}

// Recent returns up to limit of the latest records, oldest first; limit zero
// or less returns every record kept.
func (s *Store) Recent(limit int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.read()
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}
	return records, nil
}

// read parses the record files, oldest first, skipping lines that are not
// records.
func (s *Store) read() ([]Record, error) {
	var records []Record
	for i := RecordBackups; i >= 0; i-- {
		path := s.path
		if i > 0 {
			path += "." + strconv.Itoa(i)
		}
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, readRecords(f)...)
		_ = f.Close()
	}
	return records, nil
}

// maxRecordLine bounds one record line; a longer one is not a record this
// store wrote.
const maxRecordLine = 2 * maxEventSize

// readRecords reads the record lines of r, skipping lines that are too long
// or not records, so a damaged file loses only those lines.
func readRecords(r io.Reader) []Record {
	var records []Record
	reader := bufio.NewReaderSize(r, 64<<10)
	for {
		line, tooLong, err := readBoundedLine(reader, maxRecordLine)
		if !tooLong && len(line) > 0 {
			var record Record
			if json.Unmarshal(line, &record) == nil && record.Event.ID != "" {
				records = append(records, record)
			}
		}
		if err != nil {
			return records
		}
	}
}

// readBoundedLine reads one line, keeping at most limit bytes of it; tooLong
// reports a line it cut. err is set at the end of the input.
func readBoundedLine(r *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > limit {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimSpace(line), tooLong, err
	}
}

// Subscribe returns a channel that receives every record added from now on
// whose event match accepts (nil accepts all), and a function that ends the
// subscription. A subscriber that lets buffer records pile up misses later
// ones (the store logs it); Recent stays complete.
func (s *Store) Subscribe(buffer int, match func(Event) bool) (<-chan Record, func()) {
	ch := make(chan Record, buffer)
	s.mu.Lock()
	s.subs[ch] = match
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
			close(ch)
		})
	}
}

// Close closes the record file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}
