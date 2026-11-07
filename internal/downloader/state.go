package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const stateVersion = 1

// State lives next to the partial file and is what makes resume possible,
// including resume with a different (refreshed) URL.
type State struct {
	Version      int    `json:"version"`
	URL          string `json:"url"`
	FinalURL     string `json:"final_url"`
	Output       string `json:"output"`
	Part         string `json:"part"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Filename     string `json:"filename,omitempty"`
	RangeOK      bool   `json:"range_ok"`
	ChunkSize    int64  `json:"chunk_size,omitempty"`
	Done         []bool `json:"done,omitempty"`
	// ChunkBytes records how many bytes of each chunk are already on disk.
	// Saving this lets a pause/resume continue inside a chunk instead of
	// throwing away everything a connection had written so far.
	ChunkBytes []int64 `json:"chunk_bytes,omitempty"`
	DoneBytes  int64   `json:"done_bytes,omitempty"`
	HeadSHA    string  `json:"head_sha256,omitempty"`
	TailSHA    string  `json:"tail_sha256,omitempty"`
	// SHA256 is the expected checksum supplied with --sha256. Persisting it
	// means a later resume still verifies instead of silently finalizing.
	SHA256 string `json:"sha256,omitempty"`
	// VerifyFailed records that the transfer finished but the checksum did
	// not match, so resume refuses to rename the bad data into place.
	VerifyFailed bool  `json:"verify_failed,omitempty"`
	Connections  int   `json:"connections"`
	CreatedAt    int64 `json:"created_at"`
	UpdatedAt    int64 `json:"updated_at"`
}

func chunkSizeFor(size int64, conns int) int64 {
	if size <= 0 {
		return 0
	}
	target := size / int64(conns*12)
	if target < 1<<20 {
		target = 1 << 20
	}
	if target > 32<<20 {
		target = 32 << 20
	}
	if target > size {
		target = size
	}
	return target
}

func NewState(pr *ProbeResult, out, part string, conns int) *State {
	now := time.Now().Unix()
	st := &State{
		Version:      stateVersion,
		URL:          pr.FinalURL,
		FinalURL:     pr.FinalURL,
		Output:       out,
		Part:         part,
		Size:         pr.Size,
		ETag:         pr.ETag,
		LastModified: pr.LastModified,
		ContentType:  pr.ContentType,
		Filename:     pr.Filename,
		RangeOK:      pr.RangeOK,
		Connections:  conns,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if pr.RangeOK && pr.Size > 0 {
		st.ChunkSize = chunkSizeFor(pr.Size, conns)
		n := (pr.Size + st.ChunkSize - 1) / st.ChunkSize
		st.Done = make([]bool, n)
		st.ChunkBytes = make([]int64, n)
	}
	return st
}

func (s *State) chunkLen(i int64) int64 {
	start := i * s.ChunkSize
	end := start + s.ChunkSize
	if end > s.Size {
		end = s.Size
	}
	return end - start
}

// ChunkProgress is the number of bytes of chunk i already on disk.
func (s *State) ChunkProgress(i int64) int64 {
	if i < 0 {
		return 0
	}
	if int(i) < len(s.Done) && s.Done[i] {
		return s.chunkLen(i)
	}
	if int(i) < len(s.ChunkBytes) {
		p := s.ChunkBytes[i]
		if p < 0 {
			p = 0
		}
		if l := s.chunkLen(i); p > l {
			p = l
		}
		return p
	}
	return 0
}

// ProgressBytes is the best known byte count on disk, including bytes
// written into chunks that are not finished yet.
func (s *State) ProgressBytes() int64 {
	if !s.RangeOK || len(s.Done) == 0 {
		return s.DoneBytes
	}
	var n int64
	for i := range s.Done {
		n += s.ChunkProgress(int64(i))
	}
	return n
}

// CompletedBytes is the number of bytes in fully finished chunks.
func (s *State) CompletedBytes() int64 {
	if !s.RangeOK || len(s.Done) == 0 {
		return s.DoneBytes
	}
	var n int64
	for i, done := range s.Done {
		if done {
			n += s.chunkLen(int64(i))
		}
	}
	return n
}

func (s *State) Complete() bool {
	if s.Size <= 0 {
		return false
	}
	return s.CompletedBytes() >= s.Size
}

func (s *State) Save(path string) error {
	s.UpdatedAt = time.Now().Unix()
	// Compact JSON: state files are rewritten frequently during a transfer,
	// and indentation would roughly double both the CPU and the bytes written
	// for multi-thousand-chunk downloads.
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("corrupt state %s: %w", path, err)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("unsupported state version %d", st.Version)
	}
	return &st, nil
}
