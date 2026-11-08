package server

import (
	"errors"
	"os"
	"sync"

	"github.com/vult-ai/vult/ledger"
)

const maxOpenExports = 4

// Reserve room for an archive, encryption framing, and a smaller export.
const maxSpoolBytes = ledger.MaxArchiveSize + (128 << 20)

var errExportBusy = errors.New("export capacity is busy")

type exportBudget struct {
	staging sync.Mutex
	mu      sync.Mutex
	open    int
	bytes   int64
}

type exportSpool struct {
	file   *os.File
	budget *exportBudget
	size   int64
	full   bool
}

func (b *exportBudget) openSpool() (*exportSpool, error) {
	b.mu.Lock()
	if b.open >= maxOpenExports || b.bytes >= maxSpoolBytes {
		b.mu.Unlock()
		return nil, errExportBusy
	}
	b.open++
	b.mu.Unlock()

	file, err := os.CreateTemp("", "vult-book-export-*")
	if err != nil {
		b.release(0)
		return nil, err
	}
	spool := &exportSpool{file: file, budget: b}
	if err := os.Remove(file.Name()); err != nil {
		spool.close()
		return nil, err
	}
	return spool, nil
}

func (b *exportBudget) release(size int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open--
	b.bytes -= size
}

func (s *exportSpool) Write(data []byte) (int, error) {
	reserved := int64(len(data))
	s.budget.mu.Lock()
	if reserved > maxSpoolBytes-s.budget.bytes {
		s.budget.mu.Unlock()
		s.full = true
		return 0, errExportBusy
	}
	s.budget.bytes += reserved
	s.budget.mu.Unlock()

	n, err := s.file.Write(data)
	s.size += int64(n)
	if int64(n) != reserved {
		s.budget.mu.Lock()
		s.budget.bytes -= reserved - int64(n)
		s.budget.mu.Unlock()
	}
	return n, err
}

func (s *exportSpool) close() {
	_ = s.file.Close()
	s.budget.release(s.size)
}
